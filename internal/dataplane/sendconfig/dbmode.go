package sendconfig

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/blang/semver/v4"
	"github.com/go-logr/logr"
	"github.com/kong/go-database-reconciler/pkg/diff"
	"github.com/kong/go-database-reconciler/pkg/dump"
	"github.com/kong/go-database-reconciler/pkg/file"
	"github.com/kong/go-database-reconciler/pkg/state"
	deckutils "github.com/kong/go-database-reconciler/pkg/utils"
	"github.com/kong/go-kong/kong"

	"github.com/kong/kubernetes-ingress-controller/v3/internal/dataplane/deckerrors"
	"github.com/kong/kubernetes-ingress-controller/v3/internal/diagnostics"
	"github.com/kong/kubernetes-ingress-controller/v3/internal/logging"
	"github.com/kong/kubernetes-ingress-controller/v3/internal/metrics"
)

// UpdateStrategyDBMode implements the UpdateStrategy interface. It updates Kong's data-plane
// configuration using decK's syncer.
type UpdateStrategyDBMode struct {
	client            *kong.Client
	dumpConfig        dump.Config
	version           semver.Version
	concurrency       int
	diagnostic        *diagnostics.ClientDiagnostic
	isKonnect         bool
	logger            logr.Logger
	resourceErrors    []ResourceError
	resourceErrorLock *sync.Mutex
}

func NewUpdateStrategyDBMode(
	client *kong.Client,
	dumpConfig dump.Config,
	version semver.Version,
	concurrency int,
	diagnostic *diagnostics.ClientDiagnostic,
	logger logr.Logger,
) UpdateStrategyDBMode {
	return UpdateStrategyDBMode{
		client:            client,
		dumpConfig:        dumpConfig,
		version:           version,
		concurrency:       concurrency,
		diagnostic:        diagnostic,
		logger:            logger,
		resourceErrors:    []ResourceError{},
		resourceErrorLock: &sync.Mutex{},
	}
}

func NewUpdateStrategyDBModeKonnect(
	client *kong.Client,
	dumpConfig dump.Config,
	version semver.Version,
	concurrency int,
	diagnostic *diagnostics.ClientDiagnostic,
	logger logr.Logger,
) UpdateStrategyDBMode {
	s := NewUpdateStrategyDBMode(client, dumpConfig, version, concurrency, diagnostic, logger)
	s.isKonnect = true
	return s
}

func (s UpdateStrategyDBMode) Update(ctx context.Context, targetContent ContentWithHash) error {
	cs, err := s.currentState(ctx)
	if err != nil {
		return fmt.Errorf("failed getting current state for %s: %w", s.client.BaseRootURL(), err)
	}

	ts, err := s.targetState(ctx, cs, targetContent.Content)
	if err != nil {
		return deckerrors.ConfigConflictError{Err: err}
	}

	syncer, err := diff.NewSyncer(diff.SyncerOpts{
		CurrentState:        cs,
		TargetState:         ts,
		KongClient:          s.client,
		SilenceWarnings:     true,
		IsKonnect:           s.isKonnect,
		IncludeLicenses:     true,
		EnableEntityActions: true,
	})
	if err != nil {
		return fmt.Errorf("creating a new syncer for %s: %w", s.client.BaseRootURL(), err)
	}

	ctx, cancel := context.WithCancel(ctx)
	// TRR this is where db mode update strat handles events. resultchan is the entityaction channel
	// TRR targetContent.Hash is the config hash
	go s.HandleEvents(ctx, syncer.GetResultChan(), s.diagnostic, string(targetContent.Hash))

	_, errs, _ := syncer.Solve(ctx, s.concurrency, false, false)
	cancel()
	s.resourceErrorLock.Lock()
	defer s.resourceErrorLock.Unlock()
	resourceFailures := resourceErrorsToResourceFailures(s.resourceErrors, s.logger)
	if errs != nil {
		return NewUpdateError(
			resourceFailures,
			deckutils.ErrArray{Errors: errs},
		)
	}

	// as of GDR 1.8 we should always get a plain error set in addition to resourceErrors, so returning resourceErrors
	// here should not be necessary. Return it anyway as a future-proof because why not.
	if len(resourceFailures) > 0 {
		return NewUpdateError(
			resourceFailures,
			errors.New("go-database-reconciler found resource errors"),
		)
	}

	return nil
}

// TRR upstream type
//// EntityAction describes an entity processed by the diff engine and the action taken on it.
//type EntityAction struct {
//	// Action is the ReconcileAction taken on the entity.
//	Action ReconcileAction `json:"action"` // string
//	// Entity holds the processed entity.
//	Entity Entity `json:"entity"`
//	// Diff is diff string describing the modifications made to an entity.
//	Diff string `json:"-"`
//	// Error is the error encountered processing and entity, if any.
//	Error error `json:"error,omitempty"`
//}
//
//// Entity is an entity processed by the diff engine.
//type Entity struct {
//	// Name is the name of the entity.
//	Name string `json:"name"`
//	// Kind is the type of entity.
//	Kind string `json:"kind"`
//	// Old is the original entity in the current state, if any.
//	Old any `json:"old,omitempty"`
//	// New is the new entity in the target state, if any.
//	New any `json:"new,omitempty"`
//}

// HandleEvents handles logging and error reporting for individual entity change events generated during a sync by
// looping over an event channel. It terminates when its context dies.
func (s *UpdateStrategyDBMode) HandleEvents(
	ctx context.Context,
	events chan diff.EntityAction,
	diagnostic *diagnostics.ClientDiagnostic,
	hash string,
) {
	s.resourceErrorLock.Lock()
	diff := diagnostics.ConfigDiff{
		Hash:     hash,
		Entities: []diagnostics.EntityDiff{},
	}
	for {
		select {
		case event := <-events:
			if event.Error == nil {
				s.logger.V(logging.DebugLevel).Info("updated gateway entity", "action", event.Action, "kind", event.Entity.Kind, "name", event.Entity.Name)
				eventDiff := diagnostics.NewEntityDiff(event.Diff, string(event.Action))
				diff.Entities = append(diff.Entities, eventDiff)
			} else {
				s.logger.Error(event.Error, "failed updating gateway entity", "action", event.Action, "kind", event.Entity.Kind, "name", event.Entity.Name)
				parsed, err := resourceErrorFromEntityAction(event)
				if err != nil {
					s.logger.Error(err, "could not parse entity update error")
				} else {
					s.resourceErrors = append(s.resourceErrors, parsed)
				}
			}
		case <-ctx.Done():
			// The DB mode update strategy is used for both DB mode gateways and Konnect-integrated controllers. In the
			// Konnect case, we don't actually want to collect diffs, and don't actually provide a diagnostic when setting
			// it up, so we only collect and send diffs if we're talking to a gateway.
			//
			// TRR TODO maybe this is wrong? I'm not sure if we actually support (or if not, explicitly prohibit)
			// configuring a controller to use both DB mode and talk to Konnect, or if we only support DB-less when using
			// Konnect. If those are mutually exclusive, maybe we can just collect diffs for Konnect mode? If they're
			// not mutually exclusive, trying to do diagnostics diff updates for both the updates would have both attempt
			// to store diffs. This is... maybe okay. They should be identical, but that's a load-bearing "should": we know
			// Konnect can sometimes differ in what it accepts versus the gateway, and we have some Konnect configuration
			// (consumer exclude, sensitive value mask) where they're _definitely_ different. That same configuration could
			// make the diff confusing even if it's DB mode only, since it doesn't reflect what we're sending to the gateway
			// in some cases.
			if diagnostic != nil {
				diagnostic.Diffs <- diff
			}
			s.resourceErrorLock.Unlock()
			return
		}
	}
}

func resourceErrorFromEntityAction(event diff.EntityAction) (ResourceError, error) {
	var subj any
	// GDR may produce an old only (delete), new only (create), or both (update) in an event. tags should be identical
	// but we arbitrarily pull from new.
	if event.Entity.New != nil {
		subj = event.Entity.New
	} else {
		subj = event.Entity.Old
	}
	// GDR makes frequent use of "any" for its various entity handlers. It does not use interfaces that would allow us
	// to guarantee that a particular entity does indeed have tags or similar and retrieve them. We're unlikely to
	// refactor this any time soon, so in absence of proper interface methods, we pray that the entity probably has tags,
	// which is a reasonable assumption as anything KIC can manage does. The reflect-fu here is sinister and menacing,
	// but should spit out tags unless something has gone wrong.
	reflected := reflect.Indirect(reflect.ValueOf(subj))
	if reflected.Kind() != reflect.Struct {
		// We need to fail fast here because FieldByName() will panic on non-Struct Kinds.
		return ResourceError{}, fmt.Errorf("entity %s/%s is %s, not Struct",
			event.Entity.Kind, event.Entity.Name, reflected.Kind())
	}
	tagsValue := reflected.FieldByName("Tags")
	if tagsValue.IsZero() {
		return ResourceError{}, fmt.Errorf("entity %s/%s of type %s lacks 'Tags' field",
			event.Entity.Kind, event.Entity.Name, reflect.TypeOf(subj))
	}
	tags, ok := tagsValue.Interface().([]*string)
	if !ok {
		return ResourceError{}, fmt.Errorf("entity %s/%s Tags field is not []*string",
			event.Entity.Kind, event.Entity.Name)
	}

	actualTags := []string{}
	for _, s := range tags {
		actualTags = append(actualTags, *s)
	}

	// This omits ID, which should be available but requires similar reflect gymnastics as Tags, and probably isn't worth
	// it.
	raw := rawResourceError{
		Name: event.Entity.Name,
		Tags: actualTags,
		// /config flattened errors have a structured set of field to error reasons, whereas GDR errors are just plain
		// un-parsed admin API endpoint strings. These will often mention a field within the string, e.g.
		// schema violation (methods: cannot set 'methods' when 'protocols' is 'grpc' or 'grpcs')
		// has "methods", but we'd need to do string parsing to extract it, and we may not catch all possible error types.
		// This lazier approach just dumps the full error string as a single problem, which is probably good enough.
		Problems: map[string]string{
			"": fmt.Sprintf("%s", event.Error),
		},
	}

	return parseRawResourceError(raw)
}

func (s UpdateStrategyDBMode) MetricsProtocol() metrics.Protocol {
	return metrics.ProtocolDeck
}

func (s UpdateStrategyDBMode) Type() string {
	return "DBMode"
}

func (s UpdateStrategyDBMode) currentState(ctx context.Context) (*state.KongState, error) {
	rawState, err := dump.Get(ctx, s.client, s.dumpConfig)
	if err != nil {
		return nil, fmt.Errorf("loading configuration from kong: %w", err)
	}

	return state.Get(rawState)
}

func (s UpdateStrategyDBMode) targetState(
	ctx context.Context,
	currentState *state.KongState,
	targetContent *file.Content,
) (*state.KongState, error) {
	rawState, err := file.Get(ctx, targetContent, file.RenderConfig{
		CurrentState: currentState,
		KongVersion:  s.version,
	}, s.dumpConfig, s.client)
	if err != nil {
		return nil, err
	}

	return state.Get(rawState)
}

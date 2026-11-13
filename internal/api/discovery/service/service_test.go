// Copyright The Perses Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	databaseModel "github.com/perses/perses/internal/api/database/model"
	apiInterface "github.com/perses/perses/internal/api/interface"
	"github.com/perses/perses/internal/api/interface/v1/globaldatasource"
	"github.com/perses/perses/pkg/model/api"
	v1 "github.com/perses/perses/pkg/model/api/v1"
	datasourceSpec "github.com/perses/spec/go/datasource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRefreshInterval = 5 * time.Minute

// fakeClock is a deterministic clock for tests.
type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time { return f.now }

// fakeGlobalDatasourceService is an in-memory implementation of globaldatasource.Service for testing.
type fakeGlobalDatasourceService struct {
	store     map[string]*v1.GlobalDatasource
	listError error
}

var _ globaldatasource.Service = &fakeGlobalDatasourceService{}

func newFakeService(initial ...*v1.GlobalDatasource) *fakeGlobalDatasourceService {
	store := make(map[string]*v1.GlobalDatasource)
	for _, ds := range initial {
		store[ds.Metadata.Name] = ds
	}
	return &fakeGlobalDatasourceService{store: store}
}

func (f *fakeGlobalDatasourceService) Create(_ *echo.Context, entity *v1.GlobalDatasource) (*v1.GlobalDatasource, error) {
	if _, exists := f.store[entity.Metadata.Name]; exists {
		return nil, &databaseModel.Error{Code: databaseModel.ErrorCodeConflict, Key: entity.Metadata.Name}
	}
	f.store[entity.Metadata.Name] = entity
	return entity, nil
}

func (f *fakeGlobalDatasourceService) Update(_ *echo.Context, entity *v1.GlobalDatasource, _ apiInterface.Parameters) (*v1.GlobalDatasource, error) {
	f.store[entity.Metadata.Name] = entity
	return entity, nil
}

func (f *fakeGlobalDatasourceService) Delete(_ *echo.Context, parameters apiInterface.Parameters) error {
	if _, exists := f.store[parameters.Name]; !exists {
		return &databaseModel.Error{Code: databaseModel.ErrorCodeNotFound, Key: parameters.Name}
	}
	delete(f.store, parameters.Name)
	return nil
}

func (f *fakeGlobalDatasourceService) Get(parameters apiInterface.Parameters) (*v1.GlobalDatasource, error) {
	ds, ok := f.store[parameters.Name]
	if !ok {
		return nil, &databaseModel.Error{Code: databaseModel.ErrorCodeNotFound, Key: parameters.Name}
	}
	return ds, nil
}

func (f *fakeGlobalDatasourceService) List(q *globaldatasource.Query) ([]*v1.GlobalDatasource, error) {
	if f.listError != nil {
		return nil, f.listError
	}
	var filtered []*v1.GlobalDatasource
	for _, ds := range f.store {
		disc := ds.Source.GetDiscovery()
		if q.DiscoveryName != "" && (disc == nil || disc.Name != q.DiscoveryName) {
			continue
		}
		if q.DiscoveryType != "" && (disc == nil || disc.Type != q.DiscoveryType) {
			continue
		}
		filtered = append(filtered, ds)
	}
	return filtered, nil
}

func (f *fakeGlobalDatasourceService) RawList(_ *globaldatasource.Query) ([]json.RawMessage, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeGlobalDatasourceService) MetadataList(_ *globaldatasource.Query) ([]api.Entity, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeGlobalDatasourceService) RawMetadataList(_ *globaldatasource.Query) ([]json.RawMessage, error) {
	return nil, fmt.Errorf("not implemented")
}

// helpers

func makeEntity(name string) *v1.GlobalDatasource {
	return &v1.GlobalDatasource{
		Kind:     v1.KindGlobalDatasource,
		Metadata: v1.Metadata{Name: name},
		Spec:     datasourceSpec.Spec{},
	}
}

func storeEntity(name string, discoveryName string) *v1.GlobalDatasource {
	return storeEntityWithLastSeen(name, discoveryName, time.Time{})
}

func storeEntityWithLastSeen(name string, discoveryName string, lastSeen time.Time) *v1.GlobalDatasource {
	return &v1.GlobalDatasource{
		Kind:     v1.KindGlobalDatasource,
		Metadata: v1.Metadata{Name: name},
		Source: &v1.Source{
			Discovery: &v1.DiscoverySource{
				Name:     discoveryName,
				Type:     v1.DiscoveryTypeKubernetes,
				LastSeen: lastSeen,
			},
		},
		Spec: datasourceSpec.Spec{},
	}
}

func storeNames(svc *fakeGlobalDatasourceService) []string {
	var names []string
	for name := range svc.store {
		names = append(names, name)
	}
	return names
}

func newSvc(t *testing.T, fake *fakeGlobalDatasourceService) *ApplyService {
	t.Helper()
	svc, err := New(false, fake, v1.DiscoverySource{Name: "kube-prod", Type: v1.DiscoveryTypeKubernetes}, testRefreshInterval)
	require.NoError(t, err)
	return svc
}

// tests

func TestApply_StampsSourceAndDiscoveryName(t *testing.T) {
	fake := newFakeService()
	svc := newSvc(t, fake)

	require.NoError(t, svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1"), makeEntity("ds-a2")}))

	require.Len(t, fake.store, 2)
	for _, name := range []string{"ds-a1", "ds-a2"} {
		ds, ok := fake.store[name]
		require.True(t, ok)
		require.NotNil(t, ds.Source)
		require.NotNil(t, ds.Source.Discovery)
		assert.Equal(t, "kube-prod", ds.Source.Discovery.Name)
		assert.Equal(t, v1.DiscoveryTypeKubernetes, ds.Source.Discovery.Type)
		assert.False(t, ds.Source.Discovery.LastSeen.IsZero(), "LastSeen must be stamped on create")
	}
}

func TestApply_DeletesStaleEntriesScopedToDiscovery(t *testing.T) {
	now := time.Now()
	// ds-a2 was last seen more than 2× refresh_interval ago — eligible for deletion.
	stale := storeEntityWithLastSeen("ds-a2", "kube-prod", now.Add(-3*testRefreshInterval))
	fake := newFakeService(
		storeEntityWithLastSeen("ds-a1", "kube-prod", now.Add(-3*testRefreshInterval)),
		stale,
		storeEntityWithLastSeen("ds-b1", "kube-staging", now.Add(-3*testRefreshInterval)),
	)
	svc := newSvc(t, fake)
	svc.clock = &fakeClock{now: now}

	// kube-prod's new result contains only ds-a1 — ds-a2 has disappeared.
	require.NoError(t, svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1")}))

	assert.ElementsMatch(t, []string{"ds-a1", "ds-b1"}, storeNames(fake),
		"ds-a2 should be deleted; ds-b1 (owned by kube-staging) must not be touched")
}

func TestApply_RespectsGracePeriod(t *testing.T) {
	now := time.Now()
	// ds-a2 disappeared but was seen only 1× refresh_interval ago — within grace period.
	fake := newFakeService(
		storeEntityWithLastSeen("ds-a1", "kube-prod", now.Add(-3*testRefreshInterval)),
		storeEntityWithLastSeen("ds-a2", "kube-prod", now.Add(-1*testRefreshInterval)),
	)
	svc := newSvc(t, fake)
	svc.clock = &fakeClock{now: now}

	svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1")})

	assert.ElementsMatch(t, []string{"ds-a1", "ds-a2"}, storeNames(fake),
		"ds-a2 must not be deleted while still within the grace period")
}

func TestApply_NeverDeletesZeroLastSeen(t *testing.T) {
	// ds-a2 has a zero LastSeen — it was adopted but never refreshed; must not be deleted.
	fake := newFakeService(
		storeEntityWithLastSeen("ds-a1", "kube-prod", time.Now().Add(-3*testRefreshInterval)),
		storeEntity("ds-a2", "kube-prod"), // zero LastSeen
	)
	svc := newSvc(t, fake)

	svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1")})

	assert.ElementsMatch(t, []string{"ds-a1", "ds-a2"}, storeNames(fake),
		"ds-a2 must not be deleted when LastSeen is zero")
}

func TestApply_NoDeleteWhenListErrors(t *testing.T) {
	now := time.Now()
	fake := newFakeService(
		storeEntityWithLastSeen("ds-a1", "kube-prod", now.Add(-3*testRefreshInterval)),
		storeEntityWithLastSeen("ds-a2", "kube-prod", now.Add(-3*testRefreshInterval)),
	)
	fake.listError = fmt.Errorf("database unavailable")
	svc := newSvc(t, fake)
	svc.clock = &fakeClock{now: now}

	// ds-a1 is still discovered; ds-a2 is not — but List fails, so no deletion should occur.
	assert.Error(t, svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1")}))

	assert.ElementsMatch(t, []string{"ds-a1", "ds-a2"}, storeNames(fake),
		"no deletes should happen when List returns an error")
}

func TestApply_UpdatesExistingOwnedDatasource(t *testing.T) {
	now := time.Now()
	existing := storeEntityWithLastSeen("ds-a1", "kube-prod", now.Add(-2*testRefreshInterval))
	existing.Spec.Default = false
	fake := newFakeService(existing)
	svc := newSvc(t, fake)
	svc.clock = &fakeClock{now: now}

	updated := makeEntity("ds-a1")
	updated.Spec.Default = true
	require.NoError(t, svc.Apply(context.Background(), []*v1.GlobalDatasource{updated}))

	ds := fake.store["ds-a1"]
	require.NotNil(t, ds)
	assert.True(t, ds.Spec.Default, "spec should be updated")
	require.NotNil(t, ds.Source.GetDiscovery())
	assert.Equal(t, "kube-prod", ds.Source.Discovery.Name)
	assert.False(t, ds.Source.Discovery.LastSeen.IsZero(), "LastSeen must be refreshed on update")
}

func TestApply_DoesNotTakeOverAnotherDiscoverysDatasource(t *testing.T) {
	fake := newFakeService(storeEntity("ds-b1", "kube-staging"))
	svc := newSvc(t, fake)

	assert.Error(t, svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-b1")}),
		"attempting to claim another discovery's datasource should return an error")

	ds := fake.store["ds-b1"]
	require.NotNil(t, ds)
	assert.Equal(t, "kube-staging", ds.Source.Discovery.Name, "ownership must not change")
}

func TestApply_AdoptsNilSourceDatasource(t *testing.T) {
	manual := makeEntity("ds-manual")
	fake := newFakeService(manual)
	svc := newSvc(t, fake)

	require.NoError(t, svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-manual")}))

	ds := fake.store["ds-manual"]
	require.NotNil(t, ds)
	require.NotNil(t, ds.Source.GetDiscovery(), "nil-source datasource should be adopted")
	assert.Equal(t, "kube-prod", ds.Source.Discovery.Name)
}

func TestApplyService_PreservesDefault(t *testing.T) {
	now := time.Now()
	existing := storeEntityWithLastSeen("ds-a1", "kube-prod", now.Add(-2*testRefreshInterval))
	existing.Spec.Default = true
	fake := newFakeService(existing)
	svc := newSvc(t, fake)
	svc.clock = &fakeClock{now: now}

	// The newly discovered entity does NOT have Default set.
	require.NoError(t, svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1")}))

	ds := fake.store["ds-a1"]
	require.NotNil(t, ds)
	require.NotNil(t, ds.Source.GetDiscovery())
	assert.Equal(t, "kube-prod", ds.Source.Discovery.Name)
}

func TestApply_MultiReplica_SecondReplicaSkipsUpdateWhenRecentAndSpecUnchanged(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Replica 1 ran at t1 and stamped LastSeen.
	existing := storeEntityWithLastSeen("ds-a1", "kube-prod", t1)
	fake := newFakeService(existing)

	// Replica 2 runs at t1 + 30s (well within refresh_interval of 5m).
	t2 := t1.Add(30 * time.Second)
	svc := newSvc(t, fake)
	svc.clock = &fakeClock{now: t2}

	// Spec is unchanged.
	svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1")})

	// Update must NOT have been called — LastSeen should still be t1.
	ds := fake.store["ds-a1"]
	require.NotNil(t, ds)
	assert.Equal(t, t1, ds.Source.Discovery.LastSeen,
		"second replica must not overwrite LastSeen when spec is unchanged and LastSeen is recent")
}

func TestApply_MultiReplica_UpdatesWhenLastSeenExpired(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	existing := storeEntityWithLastSeen("ds-a1", "kube-prod", t1)
	fake := newFakeService(existing)

	// Replica runs after more than one refresh_interval has elapsed.
	t2 := t1.Add(testRefreshInterval + time.Second)
	svc := newSvc(t, fake)
	svc.clock = &fakeClock{now: t2}

	svc.Apply(context.Background(), []*v1.GlobalDatasource{makeEntity("ds-a1")})

	ds := fake.store["ds-a1"]
	require.NotNil(t, ds)
	assert.Equal(t, t2, ds.Source.Discovery.LastSeen,
		"LastSeen must be updated once the previous stamp has expired")
}

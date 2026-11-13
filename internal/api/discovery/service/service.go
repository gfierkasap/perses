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
	"errors"
	"time"

	databaseModel "github.com/perses/perses/internal/api/database/model"
	apiInterface "github.com/perses/perses/internal/api/interface"
	"github.com/perses/perses/internal/api/interface/v1/globaldatasource"
	v1 "github.com/perses/perses/pkg/model/api/v1"
	"github.com/sirupsen/logrus"
)

// clock abstracts time.Now so tests can inject a deterministic clock.
type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func New(caseSensitive bool, svc globaldatasource.Service, discoverySource v1.DiscoverySource, refreshInterval time.Duration) *ApplyService {
	return &ApplyService{
		caseSensitive:   caseSensitive,
		svc:             svc,
		discoverySource: discoverySource,
		refreshInterval: refreshInterval,
		clock:           realClock{},
	}
}

type ApplyService struct {
	caseSensitive   bool
	svc             globaldatasource.Service
	discoverySource v1.DiscoverySource
	refreshInterval time.Duration
	clock           clock
}

func (a *ApplyService) Apply(ctx context.Context, entities []*v1.GlobalDatasource) error {
	now := a.clock.Now()

	// currentNames tracks every datasource name present in this discovery cycle,
	// regardless of whether the write succeeded, so that transient errors do not
	// cause stale entries to be deleted before the grace period expires.
	var currentNames []string
	for _, entity := range entities {
		currentNames = append(currentNames, entity.Metadata.Name)
	}

	var errs []error
	for _, entity := range entities {
		if err := ctx.Err(); err != nil {
			return err
		}

		entity.Source = &v1.Source{
			Discovery: &v1.DiscoverySource{
				Name:     a.discoverySource.Name,
				Type:     a.discoverySource.Type,
				LastSeen: now,
			},
		}
		entity.GetMetadata().Flatten(a.caseSensitive)

		_, createErr := a.svc.Create(nil, entity)
		if createErr == nil {
			continue
		}

		if !databaseModel.IsKeyConflict(createErr) {
			errs = append(errs, createErr)
			continue
		}

		param := apiInterface.Parameters{Name: entity.Metadata.Name}
		existing, getErr := a.svc.Get(param)
		if getErr != nil {
			errs = append(errs, getErr)
			continue
		}

		existingDiscovery := existing.Source.GetDiscovery()
		if existingDiscovery != nil {
			// Another discovery owns this datasource — skip.
			if existingDiscovery.Name != a.discoverySource.Name || existingDiscovery.Type != a.discoverySource.Type {
				errs = append(errs, errors.New("skipping update of globaldatasource "+entity.Metadata.Name+": owned by a different discovery"))
				continue
			}
			// Same discovery owns it: skip if spec is unchanged and LastSeen is recent
			// enough that another replica has already refreshed it this cycle.
			if a.refreshInterval > 0 &&
				!existingDiscovery.LastSeen.IsZero() &&
				now.Sub(existingDiscovery.LastSeen) < a.refreshInterval &&
				specEqual(existing, entity) {
				continue
			}
		} else {
			// Nil source: datasource predates discovery (created manually, provisioned, or
			// created before this feature was introduced). Adopt it on first update.
			logrus.Infof("adopting globaldatasource %q: stamping discovery source %q (%s)", entity.Metadata.Name, a.discoverySource.Name, a.discoverySource.Type)
		}

		if _, updateError := a.svc.Update(nil, entity, param); updateError != nil {
			errs = append(errs, updateError)
		}
	}

	foundDatasources, err := a.svc.List(
		&globaldatasource.Query{
			DiscoveryName: a.discoverySource.Name,
			DiscoveryType: a.discoverySource.Type,
		})
	if err != nil {
		logrus.WithError(err).Error("unable to get discovered globaldatasources")
		return errors.Join(append(errs, err)...)
	}

	gracePeriod := 2 * a.refreshInterval
	for _, ds := range foundDatasources {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if containsDatasource(currentNames, ds.Metadata.Name) {
			continue
		}
		disc := ds.Source.GetDiscovery()
		// Never delete an entry that has never been seen by this discovery process.
		if disc == nil || disc.LastSeen.IsZero() {
			continue
		}
		// Only delete once the entry has been absent for at least 2 × refresh_interval.
		if gracePeriod > 0 && now.Sub(disc.LastSeen) < gracePeriod {
			continue
		}
		deleteParameters := apiInterface.Parameters{Name: ds.Metadata.Name}
		if deleteErr := a.svc.Delete(nil, deleteParameters); deleteErr != nil {
			if !databaseModel.IsKeyNotFound(deleteErr) {
				errs = append(errs, deleteErr)
			}
		}
	}

	return errors.Join(errs...)
}

// specEqual reports whether two GlobalDatasource entities have identical specs.
// Used to skip no-op updates in multi-replica deployments.
func specEqual(a, b *v1.GlobalDatasource) bool {
	return a.Spec.Default == b.Spec.Default &&
		a.Spec.Plugin.Kind == b.Spec.Plugin.Kind
}

func containsDatasource(sources []string, name string) bool {
	for _, ds := range sources {
		if ds == name {
			return true
		}
	}
	return false
}

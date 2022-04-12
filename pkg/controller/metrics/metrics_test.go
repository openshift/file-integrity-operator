/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/openshift/file-integrity-operator/pkg/controller/metrics/metricsfakes"
)

var errTest = errors.New("")

func TestRegisterMetrics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		prepare   func(*metricsfakes.FakeImpl)
		shouldErr bool
	}{
		{ // success
			prepare: func(*metricsfakes.FakeImpl) {},
		},
		{ // error Register fails
			prepare: func(mock *metricsfakes.FakeImpl) {
				mock.RegisterReturns(errTest)
			},
			shouldErr: true,
		},
	} {
		mock := &metricsfakes.FakeImpl{}
		tc.prepare(mock)

		sut := NewControllerMetrics()
		sut.impl = mock

		err := sut.Register()

		if tc.shouldErr {
			require.NotNil(t, err)
		} else {
			require.Nil(t, err)
		}
	}
}

func TestFileIntegrityMetrics(t *testing.T) {
	t.Parallel()

	getMetricValue := func(col prometheus.Collector) int {
		c := make(chan prometheus.Metric, 1)
		col.Collect(c)
		m := dto.Metric{}
		err := (<-c).Write(&m)
		require.Nil(t, err)
		return int(*m.Counter.Value)
	}

	for _, tc := range []struct {
		when func(m *Metrics)
		then func(m *Metrics)
	}{
		{ // single active
			when: func(m *Metrics) {
				m.IncFileIntegrityPhaseActive()
			},
			then: func(m *Metrics) {
				ctr, err := m.metricFileIntegrityPhase.GetMetricWithLabelValues(metricLabelValuePhaseActive)
				require.Nil(t, err)
				require.Equal(t, 1, getMetricValue(ctr))
			},
		},
		{ // single error
			when: func(m *Metrics) {
				m.IncFileIntegrityError("foo")
			},
			then: func(m *Metrics) {
				ctr, err := m.metricFileIntegrityError.GetMetricWithLabelValues("foo")
				require.Nil(t, err)
				require.Equal(t, 1, getMetricValue(ctr))
			},
		},
		{ // multiple active and errors
			when: func(m *Metrics) {
				m.IncFileIntegrityPhaseActive()
				m.IncFileIntegrityPhaseActive()
				m.IncFileIntegrityError("bar")
				m.IncFileIntegrityPhaseError()
			},
			then: func(m *Metrics) {
				phaseActive, err := m.metricFileIntegrityPhase.GetMetricWithLabelValues(metricLabelValuePhaseActive)
				require.Nil(t, err)
				require.Equal(t, 2, getMetricValue(phaseActive))

				phaseError, err := m.metricFileIntegrityPhase.GetMetricWithLabelValues(metricLabelValuePhaseError)
				require.Nil(t, err)
				require.Equal(t, 1, getMetricValue(phaseError))

				integError, err := m.metricFileIntegrityError.GetMetricWithLabelValues("bar")
				require.Nil(t, err)
				require.Equal(t, 1, getMetricValue(integError))
			},
		},
	} {
		mock := &metricsfakes.FakeImpl{}
		sut := NewControllerMetrics()
		sut.impl = mock

		tc.when(sut)
		tc.then(sut)
	}
}

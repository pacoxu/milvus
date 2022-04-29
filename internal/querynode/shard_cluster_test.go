// Licensed to the LF AI & Data foundation under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership. The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package querynode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/milvus-io/milvus/internal/proto/commonpb"
	"github.com/milvus-io/milvus/internal/proto/internalpb"
	"github.com/milvus-io/milvus/internal/proto/querypb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockNodeDetector struct {
	initNodes []nodeEvent
	evtCh     chan nodeEvent
}

func (m *mockNodeDetector) watchNodes(collectionID int64, replicaID int64, vchannelName string) ([]nodeEvent, <-chan nodeEvent) {
	return m.initNodes, m.evtCh
}

type mockSegmentDetector struct {
	initSegments []segmentEvent
	evtCh        chan segmentEvent
}

func (m *mockSegmentDetector) watchSegments(collectionID int64, replicaID int64, vchannelName string) ([]segmentEvent, <-chan segmentEvent) {
	return m.initSegments, m.evtCh
}

type mockShardQueryNode struct {
	searchResult          *internalpb.SearchResults
	searchErr             error
	queryResult           *internalpb.RetrieveResults
	queryErr              error
	releaseSegmentsResult *commonpb.Status
	releaseSegmentsErr    error
}

func (m *mockShardQueryNode) Search(_ context.Context, _ *querypb.SearchRequest) (*internalpb.SearchResults, error) {
	return m.searchResult, m.searchErr
}

func (m *mockShardQueryNode) Query(_ context.Context, _ *querypb.QueryRequest) (*internalpb.RetrieveResults, error) {
	return m.queryResult, m.queryErr
}

func (m *mockShardQueryNode) ReleaseSegments(ctx context.Context, in *querypb.ReleaseSegmentsRequest) (*commonpb.Status, error) {
	return m.releaseSegmentsResult, m.releaseSegmentsErr
}

func (m *mockShardQueryNode) Stop() error {
	return nil
}

func buildMockQueryNode(nodeID int64, addr string) shardQueryNode {
	return &mockShardQueryNode{
		searchResult: &internalpb.SearchResults{},
		queryResult:  &internalpb.RetrieveResults{},
	}
}

func TestShardCluster_Create(t *testing.T) {
	collectionID := int64(1)
	vchannelName := "dml_1_1_v0"
	replicaID := int64(0)

	t.Run("empty shard cluster", func(t *testing.T) {
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{}, buildMockQueryNode)
		assert.NotPanics(t, func() { sc.Close() })
		// close twice
		assert.NotPanics(t, func() { sc.Close() })
	})

	t.Run("init nodes", func(t *testing.T) {
		nodeEvent := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvent,
			}, &mockSegmentDetector{}, buildMockQueryNode)
		defer sc.Close()

		for _, e := range nodeEvent {
			node, has := sc.getNode(e.nodeID)
			assert.True(t, has)
			assert.Equal(t, e.nodeAddr, node.nodeAddr)
		}
	})

	t.Run("init segments", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoading,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)
		defer sc.Close()

		for _, e := range segmentEvents {
			sc.mut.RLock()
			segment, has := sc.segments[e.segmentID]
			sc.mut.RUnlock()
			assert.True(t, has)
			assert.Equal(t, e.segmentID, segment.segmentID)
			assert.Equal(t, e.nodeID, segment.nodeID)
			assert.Equal(t, e.state, segment.state)
		}
		assert.EqualValues(t, unavailable, sc.state.Load())
	})
}

func TestShardCluster_nodeEvent(t *testing.T) {
	collectionID := int64(1)
	vchannelName := "dml_1_1_v0"
	replicaID := int64(0)

	t.Run("only nodes", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}
		evtCh := make(chan nodeEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
				evtCh:     evtCh,
			}, &mockSegmentDetector{}, buildMockQueryNode)
		defer sc.Close()

		evtCh <- nodeEvent{
			nodeID:    3,
			nodeAddr:  "addr_3",
			eventType: nodeAdd,
		}
		// same event
		evtCh <- nodeEvent{
			nodeID:    3,
			nodeAddr:  "addr_3",
			eventType: nodeAdd,
		}

		assert.Eventually(t, func() bool {
			node, has := sc.getNode(3)
			return has && node.nodeAddr == "addr_3"
		}, time.Second, time.Millisecond)

		evtCh <- nodeEvent{
			nodeID:    3,
			nodeAddr:  "addr_new",
			eventType: nodeAdd,
		}
		assert.Eventually(t, func() bool {
			node, has := sc.getNode(3)
			return has && node.nodeAddr == "addr_new"
		}, time.Second, time.Millisecond)

		evtCh <- nodeEvent{
			nodeID:    3,
			nodeAddr:  "addr_new",
			eventType: nodeDel,
		}
		assert.Eventually(t, func() bool {
			_, has := sc.getNode(3)
			return !has
		}, time.Second, time.Millisecond)
		assert.Equal(t, int32(available), sc.state.Load())

		evtCh <- nodeEvent{
			nodeID:    4,
			nodeAddr:  "addr_new",
			eventType: nodeDel,
		}

		close(evtCh)
	})

	t.Run("with segments", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoading,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}

		evtCh := make(chan nodeEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
				evtCh:     evtCh,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)
		defer sc.Close()

		evtCh <- nodeEvent{
			nodeID:    3,
			nodeAddr:  "addr_3",
			eventType: nodeAdd,
		}

		assert.Eventually(t, func() bool {
			node, has := sc.getNode(3)
			return has && node.nodeAddr == "addr_3"
		}, time.Second, time.Millisecond)

		evtCh <- nodeEvent{
			nodeID:    3,
			nodeAddr:  "addr_new",
			eventType: nodeAdd,
		}
		assert.Eventually(t, func() bool {
			node, has := sc.getNode(3)
			return has && node.nodeAddr == "addr_new"
		}, time.Second, time.Millisecond)

		// remove node 2
		evtCh <- nodeEvent{
			nodeID:    2,
			nodeAddr:  "addr_2",
			eventType: nodeDel,
		}
		assert.Eventually(t, func() bool {
			_, has := sc.getNode(2)
			return !has
		}, time.Second, time.Millisecond)
		assert.Equal(t, int32(unavailable), sc.state.Load())

		segment, has := sc.getSegment(2)
		assert.True(t, has)
		assert.Equal(t, segmentStateOffline, segment.state)

		close(evtCh)
	})
}

func TestShardCluster_segmentEvent(t *testing.T) {
	collectionID := int64(1)
	vchannelName := "dml_1_1_v0"
	replicaID := int64(0)

	t.Run("from loading", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoading,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoading,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateLoading,
			},
		}

		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		evtCh <- segmentEvent{
			segmentID: 1,
			nodeID:    1,
			state:     segmentStateLoading,
			eventType: segmentAdd,
		}

		evtCh <- segmentEvent{
			segmentID: 2,
			nodeID:    2,
			state:     segmentStateLoaded,
			eventType: segmentAdd,
		}

		evtCh <- segmentEvent{
			segmentID: 3,
			nodeID:    3,
			state:     segmentStateOffline,
			eventType: segmentAdd,
		}
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(2)
			return has && seg.nodeID == 2 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(3)
			return has && seg.nodeID == 3 && seg.state == segmentStateOffline
		}, time.Second, time.Millisecond)
		// put this check behind other make sure event is processed
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(1)
			return has && seg.nodeID == 1 && seg.state == segmentStateLoading
		}, time.Second, time.Millisecond)

		// node id not match
		evtCh <- segmentEvent{
			segmentID: 1,
			nodeID:    2,
			state:     segmentStateLoaded,
			eventType: segmentAdd,
		}
		// will not change
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(1)
			return has && seg.nodeID == 1 && seg.state == segmentStateLoading
		}, time.Second, time.Millisecond)
		close(evtCh)
	})

	t.Run("from loaded", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateLoaded,
			},
		}

		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		evtCh <- segmentEvent{
			segmentID: 2,
			nodeID:    2,
			state:     segmentStateLoaded,
			eventType: segmentAdd,
		}

		evtCh <- segmentEvent{
			segmentID: 1,
			nodeID:    1,
			state:     segmentStateLoading,
			eventType: segmentAdd,
		}

		evtCh <- segmentEvent{
			segmentID: 2,
			nodeID:    2,
			state:     segmentStateLoaded,
			eventType: segmentAdd,
		}

		evtCh <- segmentEvent{
			segmentID: 3,
			nodeID:    3,
			state:     segmentStateOffline,
			eventType: segmentAdd,
		}
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(1)
			return has && seg.nodeID == 1 && seg.state == segmentStateLoading
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(3)
			return has && seg.nodeID == 3 && seg.state == segmentStateOffline
		}, time.Second, time.Millisecond)
		// put this check behind other make sure event is processed
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(2)
			return has && seg.nodeID == 2 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)

	})

	t.Run("from offline", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateOffline,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateOffline,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}

		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()
		evtCh <- segmentEvent{
			segmentID: 3,
			nodeID:    3,
			state:     segmentStateOffline,
			eventType: segmentAdd,
		}
		evtCh <- segmentEvent{
			segmentID: 1,
			nodeID:    1,
			state:     segmentStateLoading,
			eventType: segmentAdd,
		}

		evtCh <- segmentEvent{
			segmentID: 2,
			nodeID:    2,
			state:     segmentStateLoaded,
			eventType: segmentAdd,
		}

		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(1)
			return has && seg.nodeID == 1 && seg.state == segmentStateLoading
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(2)
			return has && seg.nodeID == 2 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)
		// put this check behind other make sure event is processed
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(3)
			return has && seg.nodeID == 3 && seg.state == segmentStateOffline
		}, time.Second, time.Millisecond)

		close(evtCh)
	})

	t.Run("remove segments", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoading,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}

		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		evtCh <- segmentEvent{
			segmentID: 3,
			nodeID:    3,
			eventType: segmentDel,
		}
		evtCh <- segmentEvent{
			segmentID: 1,
			nodeID:    1,
			eventType: segmentDel,
		}
		evtCh <- segmentEvent{
			segmentID: 2,
			nodeID:    2,
			eventType: segmentDel,
		}

		assert.Eventually(t, func() bool {
			_, has := sc.getSegment(1)
			return !has
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			_, has := sc.getSegment(2)
			return !has
		}, time.Second, time.Millisecond)
		// put this check behind other make sure event is processed
		assert.Eventually(t, func() bool {
			_, has := sc.getSegment(3)
			return !has
		}, time.Second, time.Millisecond)
	})

	t.Run("remove failed", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoading,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}

		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		// non-exist segment
		evtCh <- segmentEvent{
			segmentID: 4,
			nodeID:    4,
			eventType: segmentDel,
		}
		// segment node id not match
		evtCh <- segmentEvent{
			segmentID: 3,
			nodeID:    4,
			eventType: segmentDel,
		}

		// use add segment as event process signal
		evtCh <- segmentEvent{
			segmentID: 2,
			nodeID:    2,
			state:     segmentStateLoaded,
			eventType: segmentAdd,
		}
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(2)
			return has && seg.nodeID == 2 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)

		_, has := sc.getSegment(3)
		assert.True(t, has)
	})
}

func TestShardCluster_SyncSegments(t *testing.T) {
	collectionID := int64(1)
	vchannelName := "dml_1_1_v0"
	replicaID := int64(0)

	t.Run("sync new segments", func(t *testing.T) {
		segmentEvents := []segmentEvent{}

		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		sc.SyncSegments([]*querypb.ReplicaSegmentsInfo{
			{
				NodeId:     1,
				SegmentIds: []int64{1},
			},
			{
				NodeId:     2,
				SegmentIds: []int64{2},
			},
			{
				NodeId:     3,
				SegmentIds: []int64{3},
			},
		}, segmentStateLoaded)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(1)
			return has && seg.nodeID == 1 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(2)
			return has && seg.nodeID == 2 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(3)
			return has && seg.nodeID == 3 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)

	})

	t.Run("sync existing segments", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateOffline,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateOffline,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}

		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		sc.SyncSegments([]*querypb.ReplicaSegmentsInfo{
			{
				NodeId:     1,
				SegmentIds: []int64{1},
			},
			{
				NodeId:     2,
				SegmentIds: []int64{2},
			},
			{
				NodeId:     3,
				SegmentIds: []int64{3},
			},
		}, segmentStateLoaded)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(1)
			return has && seg.nodeID == 1 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(2)
			return has && seg.nodeID == 2 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)
		assert.Eventually(t, func() bool {
			seg, has := sc.getSegment(3)
			return has && seg.nodeID == 3 && seg.state == segmentStateLoaded
		}, time.Second, time.Millisecond)
	})

}

func TestShardCluster_Search(t *testing.T) {
	collectionID := int64(1)
	vchannelName := "dml_1_1_v0"
	replicaID := int64(0)
	ctx := context.Background()

	t.Run("search unavailable cluster", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateOffline,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateOffline,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)

		defer sc.Close()
		require.EqualValues(t, unavailable, sc.state.Load())

		_, err := sc.Search(ctx, &querypb.SearchRequest{
			DmlChannel: vchannelName,
		})
		assert.Error(t, err)
	})

	t.Run("search wrong channel", func(t *testing.T) {
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{}, buildMockQueryNode)

		defer sc.Close()

		_, err := sc.Search(ctx, &querypb.SearchRequest{
			DmlChannel: vchannelName + "_suffix",
		})
		assert.Error(t, err)
	})

	t.Run("normal search", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 3,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)

		defer sc.Close()
		require.EqualValues(t, available, sc.state.Load())

		result, err := sc.Search(ctx, &querypb.SearchRequest{
			DmlChannel: vchannelName,
		})
		assert.NoError(t, err)
		assert.Equal(t, len(nodeEvents), len(result))
	})

	t.Run("partial fail", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 3,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, func(nodeID int64, addr string) shardQueryNode {
				if nodeID != 2 { // hard code error one
					return buildMockQueryNode(nodeID, addr)
				}
				return &mockShardQueryNode{
					searchErr: errors.New("mocked error"),
					queryErr:  errors.New("mocked error"),
				}
			})

		defer sc.Close()
		require.EqualValues(t, available, sc.state.Load())

		_, err := sc.Search(ctx, &querypb.SearchRequest{
			DmlChannel: vchannelName,
		})
		assert.Error(t, err)
	})

	t.Run("test meta error", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
			// segment belongs to node not registered
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateLoaded,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)

		defer sc.Close()
		require.EqualValues(t, available, sc.state.Load())

		_, err := sc.Search(ctx, &querypb.SearchRequest{
			DmlChannel: vchannelName,
		})
		assert.Error(t, err)
	})
}

func TestShardCluster_Query(t *testing.T) {
	collectionID := int64(1)
	vchannelName := "dml_1_1_v0"
	replicaID := int64(0)
	ctx := context.Background()

	t.Run("query unavailable cluster", func(t *testing.T) {
		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateOffline,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateOffline,
			},
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateOffline,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)

		defer sc.Close()
		require.EqualValues(t, unavailable, sc.state.Load())

		_, err := sc.Query(ctx, &querypb.QueryRequest{
			DmlChannel: vchannelName,
		})
		assert.Error(t, err)
	})
	t.Run("query wrong channel", func(t *testing.T) {
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{}, &mockSegmentDetector{}, buildMockQueryNode)

		defer sc.Close()

		_, err := sc.Query(ctx, &querypb.QueryRequest{
			DmlChannel: vchannelName + "_suffix",
		})
		assert.Error(t, err)
	})
	t.Run("normal query", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 3,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)

		defer sc.Close()
		require.EqualValues(t, available, sc.state.Load())

		result, err := sc.Query(ctx, &querypb.QueryRequest{
			DmlChannel: vchannelName,
		})
		assert.NoError(t, err)
		assert.Equal(t, len(nodeEvents), len(result))
	})

	t.Run("partial fail", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 3,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, func(nodeID int64, addr string) shardQueryNode {
				if nodeID != 2 { // hard code error one
					return buildMockQueryNode(nodeID, addr)
				}
				return &mockShardQueryNode{
					searchErr: errors.New("mocked error"),
					queryErr:  errors.New("mocked error"),
				}
			})

		defer sc.Close()
		require.EqualValues(t, available, sc.state.Load())

		_, err := sc.Query(ctx, &querypb.QueryRequest{
			DmlChannel: vchannelName,
		})
		assert.Error(t, err)
	})
	t.Run("test meta error", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
			// segment belongs to node not registered
			{
				segmentID: 3,
				nodeID:    3,
				state:     segmentStateLoaded,
			},
		}

		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)

		defer sc.Close()
		require.EqualValues(t, available, sc.state.Load())

		_, err := sc.Query(ctx, &querypb.QueryRequest{
			DmlChannel: vchannelName,
		})
		assert.Error(t, err)
	})

}

func TestShardCluster_ReferenceCount(t *testing.T) {
	collectionID := int64(1)
	vchannelName := "dml_1_1_v0"
	replicaID := int64(0)
	//	ctx := context.Background()

	t.Run("normal alloc & finish", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)
		defer sc.Close()

		allocs := sc.segmentAllocations(nil)

		sc.mut.RLock()
		for _, segment := range sc.segments {
			assert.Greater(t, segment.inUse, int32(0))
		}
		sc.mut.RUnlock()

		assert.True(t, sc.segmentsInUse([]int64{1, 2}))
		assert.True(t, sc.segmentsInUse([]int64{1, 2, -1}))

		sc.finishUsage(allocs)
		sc.mut.RLock()
		for _, segment := range sc.segments {
			assert.EqualValues(t, segment.inUse, 0)
		}
		sc.mut.RUnlock()

		assert.False(t, sc.segmentsInUse([]int64{1, 2}))
		assert.False(t, sc.segmentsInUse([]int64{1, 2, -1}))
	})

	t.Run("alloc & finish with modified alloc", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)
		defer sc.Close()

		allocs := sc.segmentAllocations(nil)

		sc.mut.RLock()
		for _, segment := range sc.segments {
			assert.Greater(t, segment.inUse, int32(0))
		}
		sc.mut.RUnlock()

		for node, segments := range allocs {
			segments = append(segments, -1) // add non-exist segment
			// shall be ignored in finishUsage
			allocs[node] = segments
		}

		assert.NotPanics(t, func() {
			sc.finishUsage(allocs)
		})
		sc.mut.RLock()
		for _, segment := range sc.segments {
			assert.EqualValues(t, segment.inUse, 0)
		}
		sc.mut.RUnlock()
	})

	t.Run("wait segments online", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		assert.True(t, sc.segmentsOnline([]int64{1, 2}))
		assert.False(t, sc.segmentsOnline([]int64{1, 2, 3}))

		sig := make(chan struct{})
		go func() {
			sc.waitSegmentsOnline([]int64{1, 2, 3})
			close(sig)
		}()

		evtCh <- segmentEvent{
			eventType: segmentAdd,
			segmentID: 3,
			nodeID:    1,
			state:     segmentStateLoaded,
		}

		<-sig
		assert.True(t, sc.segmentsOnline([]int64{1, 2, 3}))
	})
}

func TestShardCluster_HandoffSegments(t *testing.T) {
	collectionID := int64(1)
	otherCollectionID := int64(2)
	vchannelName := "dml_1_1_v0"
	otherVchannelName := "dml_1_2_v0"
	replicaID := int64(0)

	t.Run("handoff without using segments", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)
		defer sc.Close()

		err := sc.HandoffSegments(&querypb.SegmentChangeInfo{
			OnlineSegments: []*querypb.SegmentInfo{
				{SegmentID: 2, NodeID: 2, CollectionID: collectionID, DmChannel: vchannelName},
			},
			OfflineSegments: []*querypb.SegmentInfo{
				{SegmentID: 1, NodeID: 1, CollectionID: collectionID, DmChannel: vchannelName},
			},
		})
		if err != nil {
			t.Log(err.Error())
		}
		assert.NoError(t, err)

		sc.mut.RLock()
		_, has := sc.segments[1]
		sc.mut.RUnlock()

		assert.False(t, has)
	})
	t.Run("handoff with growing segment(segment not recorded)", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
			}, buildMockQueryNode)
		defer sc.Close()

		err := sc.HandoffSegments(&querypb.SegmentChangeInfo{
			OnlineSegments: []*querypb.SegmentInfo{
				{SegmentID: 2, NodeID: 2, CollectionID: collectionID, DmChannel: vchannelName},
				{SegmentID: 4, NodeID: 2, CollectionID: otherCollectionID, DmChannel: otherVchannelName},
			},
			OfflineSegments: []*querypb.SegmentInfo{
				{SegmentID: 3, NodeID: 1, CollectionID: collectionID, DmChannel: vchannelName},
				{SegmentID: 5, NodeID: 2, CollectionID: otherCollectionID, DmChannel: otherVchannelName},
			},
		})
		assert.NoError(t, err)

		sc.mut.RLock()
		_, has := sc.segments[3]
		sc.mut.RUnlock()

		assert.False(t, has)
	})
	t.Run("handoff wait online and usage", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		// add rc to all segments
		allocs := sc.segmentAllocations(nil)

		sig := make(chan struct{})
		go func() {
			err := sc.HandoffSegments(&querypb.SegmentChangeInfo{
				OnlineSegments: []*querypb.SegmentInfo{
					{SegmentID: 3, NodeID: 1, CollectionID: collectionID, DmChannel: vchannelName},
				},
				OfflineSegments: []*querypb.SegmentInfo{
					{SegmentID: 1, NodeID: 1, CollectionID: collectionID, DmChannel: vchannelName},
				},
			})

			assert.NoError(t, err)
			close(sig)
		}()

		sc.mut.RLock()
		// still waiting online
		assert.Equal(t, 0, len(sc.handoffs))
		sc.mut.RUnlock()

		evtCh <- segmentEvent{
			eventType: segmentAdd,
			segmentID: 3,
			nodeID:    1,
			state:     segmentStateLoaded,
		}

		// wait for handoff appended into list
		assert.Eventually(t, func() bool {
			sc.mut.RLock()
			defer sc.mut.RUnlock()
			return len(sc.handoffs) > 0
		}, time.Second, time.Millisecond*10)

		tmpAllocs := sc.segmentAllocations(nil)
		found := false
		for _, segments := range tmpAllocs {
			if inList(segments, int64(1)) {
				found = true
				break
			}
		}
		// segment 1 shall not be allocated again!
		assert.False(t, found)
		sc.finishUsage(tmpAllocs)
		// rc shall be 0 now
		sc.finishUsage(allocs)

		// wait handoff finished
		<-sig

		sc.mut.RLock()
		_, has := sc.segments[1]
		sc.mut.RUnlock()

		assert.False(t, has)
	})

	t.Run("handoff from non-exist node", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		evtCh := make(chan segmentEvent, 10)
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, buildMockQueryNode)
		defer sc.Close()

		err := sc.HandoffSegments(&querypb.SegmentChangeInfo{
			OnlineSegments: []*querypb.SegmentInfo{
				{SegmentID: 2, NodeID: 2, CollectionID: collectionID, DmChannel: vchannelName},
			},
			OfflineSegments: []*querypb.SegmentInfo{
				{SegmentID: 1, NodeID: 3, CollectionID: collectionID, DmChannel: vchannelName},
			},
		})

		assert.Error(t, err)
	})

	t.Run("release failed", func(t *testing.T) {
		nodeEvents := []nodeEvent{
			{
				nodeID:   1,
				nodeAddr: "addr_1",
			},
			{
				nodeID:   2,
				nodeAddr: "addr_2",
			},
		}

		segmentEvents := []segmentEvent{
			{
				segmentID: 1,
				nodeID:    1,
				state:     segmentStateLoaded,
			},
			{
				segmentID: 2,
				nodeID:    2,
				state:     segmentStateLoaded,
			},
		}
		evtCh := make(chan segmentEvent, 10)
		mqn := &mockShardQueryNode{}
		sc := NewShardCluster(collectionID, replicaID, vchannelName,
			&mockNodeDetector{
				initNodes: nodeEvents,
			}, &mockSegmentDetector{
				initSegments: segmentEvents,
				evtCh:        evtCh,
			}, func(nodeID int64, addr string) shardQueryNode {
				return mqn
			})
		defer sc.Close()

		mqn.releaseSegmentsErr = errors.New("mocked error")

		err := sc.HandoffSegments(&querypb.SegmentChangeInfo{
			OfflineSegments: []*querypb.SegmentInfo{
				{SegmentID: 1, NodeID: 1, CollectionID: collectionID, DmChannel: vchannelName},
			},
		})

		assert.Error(t, err)

		mqn.releaseSegmentsErr = nil
		mqn.releaseSegmentsResult = &commonpb.Status{
			ErrorCode: commonpb.ErrorCode_UnexpectedError,
			Reason:    "mocked error",
		}

		err = sc.HandoffSegments(&querypb.SegmentChangeInfo{
			OfflineSegments: []*querypb.SegmentInfo{
				{SegmentID: 2, NodeID: 2, CollectionID: collectionID, DmChannel: vchannelName},
			},
		})

		assert.Error(t, err)

	})
}
package main

import (
	"errors"
	"reflect"
	"testing"
)

type recordingRepository struct {
	updates   []BizService
	adds      []BizService
	deletes   []BizService
	updateErr error
	addErr    error
	deleteErr error
}

func (r *recordingRepository) Update(item BizService) error {
	r.updates = append(r.updates, item)
	return r.updateErr
}

func (r *recordingRepository) Add(items []BizService) error {
	r.adds = append(r.adds, items...)
	return r.addErr
}

func (r *recordingRepository) Delete(items []BizService) error {
	r.deletes = append(r.deletes, items...)
	return r.deleteErr
}

func TestSyncUpdatesSameNameServicesIndependently(t *testing.T) {
	dp := newDataProvider(Flags{})
	oldVM := BizService{Base: Base{ID: 11, Lcuuid: "keep-vm"}, Name: "app", Type: BIZ_SERVICE_TYPE_CHOST,
		Resources: "old-vm", VPCIDs: AutoSplitedInts{1}, TeamID: 7, SystemID: 8, UserID: 9}
	oldPodGroup := BizService{Base: Base{ID: 22, Lcuuid: "keep-pg"}, Name: "app", Type: BIZ_SERVICE_TYPE_POD_GROUP,
		Resources: "old-pg", PodClusterIDs: AutoSplitedInts{1}, PodNamespaceIDs: AutoSplitedInts{2}}
	if err := dp.indexBizServices([]BizService{oldVM, oldPodGroup}); err != nil {
		t.Fatal(err)
	}
	dp.vmAppIdToItem["app"] = BizService{Name: "app", Type: BIZ_SERVICE_TYPE_CHOST,
		Resources: "new-vm", VPCIDs: AutoSplitedInts{10}, TeamID: 1, SystemID: 1, UserID: 1}
	dp.podGroupAppIdToItem["app"] = BizService{Name: "app", Type: BIZ_SERVICE_TYPE_POD_GROUP,
		Resources: "new-pg", PodClusterIDs: AutoSplitedInts{20}, PodNamespaceIDs: AutoSplitedInts{30}}
	repo := &recordingRepository{}
	if err := (&Synchroizer{dp: dp, repo: repo}).sync(); err != nil {
		t.Fatal(err)
	}
	if len(repo.updates) != 2 || len(repo.adds) != 0 || len(repo.deletes) != 0 {
		t.Fatalf("updates=%v adds=%v deletes=%v", repo.updates, repo.adds, repo.deletes)
	}
	for _, item := range repo.updates {
		switch item.Type {
		case BIZ_SERVICE_TYPE_CHOST:
			if item.ID != 11 || item.Lcuuid != "keep-vm" || item.Resources != "new-vm" ||
				!reflect.DeepEqual(item.VPCIDs, AutoSplitedInts{10}) || item.TeamID != 7 || item.SystemID != 8 || item.UserID != 9 {
				t.Fatalf("incorrect VM update: %+v", item)
			}
		case BIZ_SERVICE_TYPE_POD_GROUP:
			if item.ID != 22 || item.Lcuuid != "keep-pg" || item.Resources != "new-pg" ||
				!reflect.DeepEqual(item.PodClusterIDs, AutoSplitedInts{20}) ||
				!reflect.DeepEqual(item.PodNamespaceIDs, AutoSplitedInts{30}) {
				t.Fatalf("incorrect pod group update: %+v", item)
			}
		default:
			t.Fatalf("unexpected service type: %d", item.Type)
		}
	}
}

func TestScopeOnlyChangesTriggerUpdate(t *testing.T) {
	tests := []struct {
		name string
		old  BizService
		new  BizService
	}{
		{"VPC", BizService{Type: BIZ_SERVICE_TYPE_CHOST, Resources: "vm", VPCIDs: AutoSplitedInts{1}},
			BizService{Type: BIZ_SERVICE_TYPE_CHOST, Resources: "vm", VPCIDs: AutoSplitedInts{2}}},
		{"cluster", BizService{Type: BIZ_SERVICE_TYPE_POD_GROUP, Resources: "pg", PodClusterIDs: AutoSplitedInts{1}},
			BizService{Type: BIZ_SERVICE_TYPE_POD_GROUP, Resources: "pg", PodClusterIDs: AutoSplitedInts{2}}},
		{"namespace", BizService{Type: BIZ_SERVICE_TYPE_POD_GROUP, Resources: "pg", PodNamespaceIDs: AutoSplitedInts{1}},
			BizService{Type: BIZ_SERVICE_TYPE_POD_GROUP, Resources: "pg"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, changed := updatedBizService(test.old, test.new); !changed {
				t.Fatal("scope change must trigger an update")
			}
		})
	}
}

func TestReorderedResourcesAndScopesDoNotTriggerUpdate(t *testing.T) {
	old := BizService{Type: BIZ_SERVICE_TYPE_CHOST, Resources: "b,a", VPCIDs: AutoSplitedInts{2, 1}}
	newItem := BizService{Type: BIZ_SERVICE_TYPE_CHOST, Resources: "a,b", VPCIDs: AutoSplitedInts{1, 2}}
	if _, changed := updatedBizService(old, newItem); changed {
		t.Fatal("reordering must not trigger a database write")
	}
	if !reflect.DeepEqual(old.VPCIDs, AutoSplitedInts{2, 1}) {
		t.Fatal("comparison must not mutate the old scope slice")
	}
}

func TestUpdateFailurePropagatesAndStopsDelete(t *testing.T) {
	dp := newDataProvider(Flags{})
	old := BizService{Base: Base{ID: 1}, Name: "app", Type: BIZ_SERVICE_TYPE_CHOST, Resources: "old"}
	obsolete := BizService{Base: Base{ID: 2}, Name: "obsolete", Type: BIZ_SERVICE_TYPE_CHOST, Resources: "gone"}
	if err := dp.indexBizServices([]BizService{old, obsolete}); err != nil {
		t.Fatal(err)
	}
	dp.vmAppIdToItem["app"] = BizService{Name: "app", Type: BIZ_SERVICE_TYPE_CHOST, Resources: "new"}
	wantErr := errors.New("database update failed")
	repo := &recordingRepository{updateErr: wantErr}
	err := (&Synchroizer{dp: dp, repo: repo}).sync()
	if !errors.Is(err, wantErr) || len(repo.deletes) != 0 {
		t.Fatalf("err=%v deletes=%v, want update failure and no deletion", err, repo.deletes)
	}
}

func TestSameNameNewTypeIsAdded(t *testing.T) {
	dp := newDataProvider(Flags{})
	old := BizService{Base: Base{ID: 1}, Name: "app", Type: BIZ_SERVICE_TYPE_CHOST, Resources: "vm"}
	if err := dp.indexBizServices([]BizService{old}); err != nil {
		t.Fatal(err)
	}
	dp.uniqueDfVmNameToDbData["vm"] = VM{Name: "vm"}
	dp.podGroupAppIdToItem["app"] = BizService{Name: "app", Type: BIZ_SERVICE_TYPE_POD_GROUP, Resources: "pg"}
	repo := &recordingRepository{}
	if err := (&Synchroizer{dp: dp, repo: repo}).sync(); err != nil {
		t.Fatal(err)
	}
	if len(repo.adds) != 1 || repo.adds[0].Type != BIZ_SERVICE_TYPE_POD_GROUP ||
		len(repo.updates) != 0 || len(repo.deletes) != 0 {
		t.Fatalf("adds=%v updates=%v deletes=%v", repo.adds, repo.updates, repo.deletes)
	}
}

func TestDeleteRequiresAbsentAppAndAllOldResourcesGone(t *testing.T) {
	dp := newDataProvider(Flags{})
	items := []BizService{
		{Base: Base{ID: 1}, Name: "gone", Type: BIZ_SERVICE_TYPE_CHOST, Resources: "missing"},
		{Base: Base{ID: 2}, Name: "retained", Type: BIZ_SERVICE_TYPE_CHOST, Resources: "missing,live"},
		{Base: Base{ID: 3}, Name: "pod", Type: BIZ_SERVICE_TYPE_POD_GROUP, Resources: "missing"},
	}
	if err := dp.indexBizServices(items); err != nil {
		t.Fatal(err)
	}
	dp.uniqueDfVmNameToDbData["live"] = VM{Name: "live"}
	dp.unniqueDfPodGroupNameToDbData["missing"] = PodGroup{Name: "missing"}
	repo := &recordingRepository{}
	if err := (&Synchroizer{dp: dp, repo: repo}).handleDelete(); err != nil {
		t.Fatal(err)
	}
	if len(repo.deletes) != 1 || repo.deletes[0].ID != 1 {
		t.Fatalf("deletes=%v, want only ID=1", repo.deletes)
	}
}

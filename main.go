package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
)

func main() {
	parseFlags()

	err := initLogger(cfg.LogPath)
	if err != nil {
		log.Fatalf("初始化日志失败: %v", err)
	}
	defer closeLogger()

	initHTTPClient()
	logger.Printf("命令执行参数: %+v", cfg)
	err = initDB(cfg.dbFlags)
	if err != nil {
		logger.Fatalf("初始化数据库失败: %v", err)
	}

	dp := newDataProvider(cfg)
	err = dp.load()
	if err != nil {
		logger.Fatalf("加载业务服务数据失败: %v", err)
	}

	synchroizer := newSynchroizer(dp)
	err = synchroizer.sync()
	if err != nil {
		logger.Fatalf("同步数据失败: %v", err)
	}
	logger.Printf("同步数据完成")
}

func newSynchroizer(dp *dataProvider) *Synchroizer {
	return &Synchroizer{
		dp:   dp,
		repo: newRepository(db),
	}
}

type Synchroizer struct {
	dp   *dataProvider
	repo bizServiceRepository
}

type bizServiceRepository interface {
	Update(BizService) error
	Add([]BizService) error
	Delete([]BizService) error
}

func (s *Synchroizer) sync() error {
	if err := s.handleAddAndUpdate(); err != nil {
		return err
	}
	if err := s.handleDelete(); err != nil {
		return err
	}
	return nil
}

func (s *Synchroizer) handleDelete() error {
	toDelete := make([]BizService, 0)
	// 目标聚合结果中没有该应用，且所有旧资源在 DeepFlow 中均不存在时删除。
	for _, bizSvc := range s.dp.oldBizServiceKeyToItem {
		var nameOk bool
		var rscNameOk bool

		rscs := strings.Split(bizSvc.Resources, ",")

		switch bizSvc.Type {
		case BIZ_SERVICE_TYPE_CHOST:
			_, nameOk = s.dp.vmAppIdToItem[bizSvc.Name]
			for _, rsc := range rscs {
				if _, ok := s.dp.uniqueDfVmNameToDbData[rsc]; ok {
					rscNameOk = true
					break // 只要有一个存在就不用继续查了
				}
			}
		case BIZ_SERVICE_TYPE_POD_GROUP:
			_, nameOk = s.dp.podGroupAppIdToItem[bizSvc.Name]
			for _, rsc := range rscs {
				if _, ok := s.dp.unniqueDfPodGroupNameToDbData[rsc]; ok {
					rscNameOk = true
					break // 只要有一个存在就不用继续查了
				}
			}
		default:
			// 非预期的业务服务类型，跳过，避免误删
			logger.Printf("跳过未知业务服务类型: %d, 名称: %s", bizSvc.Type, bizSvc.Name)
			continue
		}

		if !nameOk && !rscNameOk {
			if s.dp.cfg.LogDebug {
				logger.Printf("删除业务服务: %+v", bizSvc)
			}
			toDelete = append(toDelete, bizSvc)
		}
	}

	if len(toDelete) > 0 {
		if err := s.repo.Delete(toDelete); err != nil {
			return err
		}
	}
	return nil
}

func (s *Synchroizer) handleAddAndUpdate() error {
	toAdd := make([]BizService, 0)
	for _, desiredItems := range []map[string]BizService{s.dp.vmAppIdToItem, s.dp.podGroupAppIdToItem} {
		for _, newItem := range desiredItems {
			key := bizServiceKey{serviceType: newItem.Type, name: newItem.Name}
			oldItem, exists := s.dp.oldBizServiceKeyToItem[key]
			if !exists {
				if s.dp.cfg.LogDebug {
					logger.Printf("新增业务服务: %+v", newItem)
				}
				toAdd = append(toAdd, newItem)
				continue
			}
			updatedItem, changed := updatedBizService(oldItem, newItem)
			if !changed {
				continue
			}
			if s.dp.cfg.LogDebug {
				logger.Printf("更新业务服务, 原数据: %+v, 新数据: %+v", oldItem, updatedItem)
			}
			if err := s.repo.Update(updatedItem); err != nil {
				return fmt.Errorf("更新业务服务 type=%d name=%q 失败: %w", oldItem.Type, oldItem.Name, err)
			}
		}
	}

	if len(toAdd) > 0 {
		if err := s.repo.Add(toAdd); err != nil {
			return err
		}
	}
	return nil
}

func updatedBizService(oldItem, newItem BizService) (BizService, bool) {
	changed := !sameResourceNames(oldItem.Resources, newItem.Resources)
	oldItem.Resources = newItem.Resources
	switch newItem.Type {
	case BIZ_SERVICE_TYPE_CHOST:
		changed = changed || !sameIntList(oldItem.VPCIDs, newItem.VPCIDs)
		oldItem.VPCIDs = newItem.VPCIDs
	case BIZ_SERVICE_TYPE_POD_GROUP:
		changed = changed || !sameIntList(oldItem.PodClusterIDs, newItem.PodClusterIDs) ||
			!sameIntList(oldItem.PodNamespaceIDs, newItem.PodNamespaceIDs)
		oldItem.PodClusterIDs = newItem.PodClusterIDs
		oldItem.PodNamespaceIDs = newItem.PodNamespaceIDs
	}
	return oldItem, changed
}

func sameResourceNames(a, b string) bool {
	left, right := strings.Split(a, ","), strings.Split(b, ",")
	sort.Strings(left)
	sort.Strings(right)
	return strings.Join(left, ",") == strings.Join(right, ",")
}

func sameIntList(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]int(nil), a...), append([]int(nil), b...)
	sort.Ints(left)
	sort.Ints(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

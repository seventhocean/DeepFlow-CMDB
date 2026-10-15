package main

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bitly/go-simplejson"
	mapset "github.com/deckarep/golang-set/v2"
	uuid "github.com/satori/go.uuid"
	"gorm.io/gorm"
)

func GenerateUUID(str string) string {
	return uuid.NewV5(uuid.NamespaceOID, str).String()
}

func newDataProvider(cfg Flags) *dataProvider {
	return &dataProvider{
		cfg: cfg,

		oldBizServiceKeyToItem:        make(map[bizServiceKey]BizService),
		vmAppIdToItem:                 make(map[string]BizService),
		podGroupAppIdToItem:           make(map[string]BizService),
		uniqueDfVmNameToDbData:        make(map[string]VM),
		unniqueDfPodGroupNameToDbData: make(map[string]PodGroup),
	}
}

type dataProvider struct {
	cfg Flags

	oldBizServiceKeyToItem        map[bizServiceKey]BizService
	vmAppIdToItem                 map[string]BizService
	podGroupAppIdToItem           map[string]BizService
	uniqueDfVmNameToDbData        map[string]VM
	unniqueDfPodGroupNameToDbData map[string]PodGroup
}

func (dp *dataProvider) load() error {
	bizServices, err := dp.getBizServices()
	if err != nil {
		return fmt.Errorf("获取业务服务数据失败: %w", err)
	}
	if err := dp.indexBizServices(bizServices); err != nil {
		return err
	}

	if err := dp.loadVmData(); err != nil {
		return err
	}
	if err := dp.loadPodGroupData(); err != nil {
		return err
	}

	return nil
}

type bizServiceKey struct {
	serviceType int
	name        string
}

func (dp *dataProvider) indexBizServices(items []BizService) error {
	indexed := make(map[bizServiceKey]BizService, len(items))
	for _, item := range items {
		key := bizServiceKey{serviceType: item.Type, name: item.Name}
		if previous, exists := indexed[key]; exists {
			return fmt.Errorf("业务服务 type=%d name=%q 存在重复记录 id=%d、%d，无法确定更新对象", item.Type, item.Name, previous.ID, item.ID)
		}
		indexed[key] = item
	}
	dp.oldBizServiceKeyToItem = indexed
	return nil
}

func (dp *dataProvider) loadVmData() error {
	dfVms, err := dp.getVms()
	if err != nil {
		return fmt.Errorf("获取 DeepFlow 云主机数据失败: %w", err)
	}
	dupNames := make([]string, 0)
	for _, item := range dfVms {
		if _, ok := dp.uniqueDfVmNameToDbData[item.Name]; ok {
			dupNames = append(dupNames, item.Name)
			continue
		}
		dp.uniqueDfVmNameToDbData[item.Name] = item
	}
	logger.Printf("DeepFlow 云主机数据加载完成，共 %d 条，其中 %d 条名称重复: %v", len(dfVms), len(dupNames), dupNames)

	cmdbVms, err := dp.getCMDBVms()
	if err != nil {
		return fmt.Errorf("获取 CMDB 云主机数据失败: %w", err)
	}
	skipNames := make([]string, 0)
	skipItems := make([]cmdbDataItem, 0)
	appIdToVmInfo := make(map[string]vmInfo)
	for _, item := range cmdbVms {
		if item.appid == "" {
			skipNames = append(skipNames, item.name)
			continue
		}
		dfVm, ok := dp.uniqueDfVmNameToDbData[item.name]
		if !ok {
			skipItems = append(skipItems, item)
			continue
		}
		if _, ok := appIdToVmInfo[item.appid]; !ok {
			appIdToVmInfo[item.appid] = vmInfo{
				names:  mapset.NewSet[string](),
				vpcIds: mapset.NewSet[int](),
			}
		}
		appIdToVmInfo[item.appid].names.Add(item.name)
		appIdToVmInfo[item.appid].vpcIds.Add(dfVm.VPCID)
	}
	for appId, vmNames := range appIdToVmInfo {
		vmNamesStr := vmNames.names.ToSlice()
		sort.Strings(vmNamesStr)
		vpcIds := vmNames.vpcIds.ToSlice()
		sort.Ints(vpcIds)
		dp.vmAppIdToItem[appId] = BizService{
			Base: Base{
				Lcuuid: GenerateUUID(appId + time.Now().GoString()),
			},
			Name:      appId,
			VPCIDs:    vpcIds,
			Resources: strings.Join(vmNamesStr, ","),
			Type:      BIZ_SERVICE_TYPE_CHOST,
			MatchType: BIZ_SERVICE_MATCH_TYPE_NAME,
			SystemID:  DEFAULT_SYSTEM_ID,
			UserID:    DEFAULT_USER_ID,
			TeamID:    DEFAULT_TEAM_ID,
		}
	}
	logger.Printf("CMDB 云主机数据加载完成，共 %d 条，其中 %d 条无 appid, 其中 %d 条无 DeepFlow 对应数据，共构建 %d 条云主机业务服务", len(cmdbVms), len(skipNames), len(skipItems), len(dp.vmAppIdToItem))
	if dp.cfg.LogDebug {
		logger.Printf("跳过云主机无 appid 数据: %v", skipNames)
		logger.Printf("跳过云主机无 DeepFlow 对应数据: %v", skipItems)
		logger.Printf("构建云主机业务服务:")
		for _, item := range dp.vmAppIdToItem {
			logger.Printf("  %+v", item)
		}
	}

	return nil
}

func (dp *dataProvider) loadPodGroupData() error {
	dfPodGroups, err := dp.getPodGroups()
	if err != nil {
		return fmt.Errorf("获取 DeepFlow 工作负载数据失败: %w", err)
	}
	dupNames := make([]string, 0)
	for _, item := range dfPodGroups {
		if _, ok := dp.unniqueDfPodGroupNameToDbData[item.Name]; ok {
			dupNames = append(dupNames, item.Name)
			continue
		}
		dp.unniqueDfPodGroupNameToDbData[item.Name] = item
	}
	logger.Printf("DeepFlow 工作负载数据加载完成，共 %d 条，其中 %d 条名称重复: %v", len(dfPodGroups), len(dupNames), dupNames)

	cmdbPodGroups, err := dp.getCMDBPodGroups()
	if err != nil {
		return fmt.Errorf("获取 CMDB 工作负载数据失败: %w", err)
	}
	skipNames := make([]string, 0)
	skipItems := make([]cmdbDataItem, 0)
	appIdToPodGroupInfo := make(map[string]podGroupInfo)
	for _, item := range cmdbPodGroups {
		if item.appid == "" {
			skipNames = append(skipNames, item.name)
			continue
		}
		dfPodGroup, ok := dp.unniqueDfPodGroupNameToDbData[item.name]
		if !ok {
			skipItems = append(skipItems, item)
			continue
		}
		if _, ok := appIdToPodGroupInfo[item.appid]; !ok {
			appIdToPodGroupInfo[item.appid] = podGroupInfo{
				names:           mapset.NewSet[string](),
				podClusterIds:   mapset.NewSet[int](),
				podNamespaceIds: mapset.NewSet[int](),
			}
		}
		appIdToPodGroupInfo[item.appid].names.Add(item.name)
		appIdToPodGroupInfo[item.appid].podClusterIds.Add(dfPodGroup.PodClusterID)
		if dfPodGroup.PodNamespaceID != 0 {
			appIdToPodGroupInfo[item.appid].podNamespaceIds.Add(dfPodGroup.PodNamespaceID)
		}
	}
	for appId, podGroupNames := range appIdToPodGroupInfo {
		podGroupNamesStr := podGroupNames.names.ToSlice()
		sort.Strings(podGroupNamesStr)
		podClusterIds := podGroupNames.podClusterIds.ToSlice()
		sort.Ints(podClusterIds)
		podNamespaceIds := podGroupNames.podNamespaceIds.ToSlice()
		sort.Ints(podNamespaceIds)
		dp.podGroupAppIdToItem[appId] = BizService{
			Base: Base{
				Lcuuid: GenerateUUID(appId + time.Now().GoString()),
			},
			Name:            appId,
			PodClusterIDs:   podClusterIds,
			PodNamespaceIDs: podNamespaceIds,
			Resources:       strings.Join(podGroupNamesStr, ","),
			Type:            BIZ_SERVICE_TYPE_POD_GROUP,
			MatchType:       BIZ_SERVICE_MATCH_TYPE_NAME,
			SystemID:        DEFAULT_SYSTEM_ID,
			UserID:          DEFAULT_USER_ID,
			TeamID:          DEFAULT_TEAM_ID,
		}
	}

	logger.Printf("CMDB 工作负载数据加载完成，共 %d 条，其中 %d 条无 appid, 其中 %d 条无 DeepFlow 对应数据, 共构建 %d 条工作负载业务服务", len(cmdbPodGroups), len(skipNames), len(skipItems), len(dp.podGroupAppIdToItem))
	if dp.cfg.LogDebug {
		logger.Printf("跳过工作负载无 appid 数据: %v", skipNames)
		logger.Printf("跳过工作负载无 DeepFlow 对应数据: %v", skipItems)
		logger.Printf("构建工作负载业务服务:")
		for _, item := range dp.podGroupAppIdToItem {
			logger.Printf("  %+v", item)
		}
	}
	return nil
}

func (dp *dataProvider) getCMDBVms() ([]cmdbDataItem, error) {
	var items []cmdbDataItem
	host := dp.cfg.HTTPHost
	if dp.cfg.HTTPPort > 0 {
		host += ":" + strconv.Itoa(int(dp.cfg.HTTPPort))
	}
	url := fmt.Sprintf("http://%s/api/v1/query/dwd/list-pa-host-b", host)
	if jsonList, err := dp.getCMDBRawData(url); err != nil {
		return nil, err
	} else {
		for _, item := range jsonList {
			items = append(items, cmdbDataItem{
				appid: item.Get("appid").MustString(),
				name:  item.Get("hostname").MustString(),
			})
		}
	}

	// items = make([]cmdbDataItem, 0, 5)
	// for i := 1; i < 6; i++ {
	// 	items = append(items, cmdbDataItem{
	// 		appid: fmt.Sprintf("1_vm_app_%d", i),
	// 		name:  fmt.Sprintf("1_vm_name_%d", i),
	// 	})
	// }
	// for i := 6; i < 8; i++ {
	// 	items = append(items, cmdbDataItem{
	// 		appid: fmt.Sprintf("1_vm_app_%d", 6),
	// 		name:  fmt.Sprintf("1_vm_name_%d", i),
	// 	})
	// }
	// for i := 8; i < 11; i++ {
	// 	items = append(items, cmdbDataItem{
	// 		appid: fmt.Sprintf("1_vm_app_%d", 8),
	// 		name:  fmt.Sprintf("1_vm_name_%d", i),
	// 	})
	// }
	logger.Printf("获取 %d 条 CMDB 云主机数据", len(items))
	return items, nil
}

func (dp *dataProvider) getCMDBPodGroups() ([]cmdbDataItem, error) {
	var items []cmdbDataItem
	host := dp.cfg.HTTPHost
	if dp.cfg.HTTPPort > 0 {
		host += ":" + strconv.Itoa(int(dp.cfg.HTTPPort))
	}
	url := fmt.Sprintf("http://%s/api/vl/query/dwd/list-worklaod-resource-by-cluster-id", host)
	if jsonList, err := dp.getCMDBRawData(url); err != nil {
		return nil, err
	} else {
		for _, item := range jsonList {
			items = append(items, cmdbDataItem{
				appid: item.Get("appid").MustString(),
				name:  item.Get("name").MustString(),
			})
		}
	}

	// items = make([]cmdbDataItem, 0, 5)
	// for i := 1; i < 6; i++ {
	// 	items = append(items, cmdbDataItem{
	// 		appid: fmt.Sprintf("1_pg_app_%d", i),
	// 		name:  fmt.Sprintf("1_pg_name_%d", i),
	// 	})
	// }
	// for i := 6; i < 8; i++ {
	// 	items = append(items, cmdbDataItem{
	// 		appid: fmt.Sprintf("1_pg_app_%d", 6),
	// 		name:  fmt.Sprintf("1_pg_name_%d", i),
	// 	})
	// }
	// for i := 8; i < 11; i++ {
	// 	items = append(items, cmdbDataItem{
	// 		appid: fmt.Sprintf("1_pg_app_%d", 8),
	// 		name:  fmt.Sprintf("1_pg_name_%d", i),
	// 	})
	// }
	logger.Printf("获取 %d 条 CMDB 工作负载数据", len(items))
	return items, nil
}

func (dp *dataProvider) getCMDBRawData(endpoint string) (jsonList []*simplejson.Json, err error) {
	if dp.cfg.PageSize == 0 {
		return nil, fmt.Errorf("分页大小必须大于 0")
	}
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("CMDB 接口地址无效: %w", err)
	}
	seenPages := make(map[[sha256.Size]byte]struct{})
	pageNum := 1
	for {
		query := parsedURL.Query()
		query.Set("pageSize", strconv.FormatUint(uint64(dp.cfg.PageSize), 10))
		query.Set("pageNum", strconv.Itoa(pageNum))
		parsedURL.RawQuery = query.Encode()
		requestURL := parsedURL.String()
		resp, err := httpclient.Get(requestURL)
		if err != nil {
			logger.Printf("url=%s, headers=%v", requestURL, dp.cfg.Headers)
			return []*simplejson.Json{}, err
		}
		jData := resp.Get("list")
		items, err := jData.Array()
		if err != nil {
			return nil, fmt.Errorf("CMDB 第 %d 页缺少有效的顶层 list 数组: %w", pageNum, err)
		}
		if len(items) == 0 {
			break
		}
		pageData, err := jData.Encode()
		if err != nil {
			return nil, fmt.Errorf("编码 CMDB 第 %d 页失败: %w", pageNum, err)
		}
		fingerprint := sha256.Sum256(pageData)
		if _, exists := seenPages[fingerprint]; exists {
			return nil, fmt.Errorf("CMDB 第 %d 页内容重复，请检查接口分页参数", pageNum)
		}
		seenPages[fingerprint] = struct{}{}
		for i := range items {
			item := jData.GetIndex(i)
			if _, err := item.Map(); err != nil {
				return nil, fmt.Errorf("CMDB 第 %d 页第 %d 条记录不是对象: %w", pageNum, i+1, err)
			}
			jsonList = append(jsonList, item)
		}
		pageNum++
	}
	return
}

// 获取所有 BizService 实例
func (dp *dataProvider) getBizServices() ([]BizService, error) {
	var bizServices []BizService
	query := db.Where(
		"type IN ? AND match_type = ?",
		[]int{BIZ_SERVICE_TYPE_CHOST, BIZ_SERVICE_TYPE_POD_GROUP},
		BIZ_SERVICE_MATCH_TYPE_NAME)
	if err := query.Find(&bizServices).Error; err != nil {
		return nil, err
	}
	sql := query.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Find(&bizServices)
	})
	logger.Printf("获取业务服务查询 sql: %s", sql)
	logger.Printf("获取 %d 条业务服务（仅包含 CHOST 和 POD_GROUP 类型，匹配类型为 名称，资源为单选）", len(bizServices))
	return bizServices, nil
}

// 获取所有 VM 数据，仅获取 id、name、epc_id 字段
func (dp *dataProvider) getVms() ([]VM, error) {
	var vms []VM
	query := db.Model(&VM{}).Select("id, name, epc_id")
	if err := query.Find(&vms).Error; err != nil {
		return nil, err
	}
	sql := query.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Find(&vms)
	})
	logger.Printf("获取云主机查询 sql: %s", sql)
	logger.Printf("获取 %d 条云主机", len(vms))
	return vms, nil
}

// 获取所有 PodGroup 数据，仅获取 id、name、pod_cluster_id、pod_namespace_id 字段
func (dp *dataProvider) getPodGroups() ([]PodGroup, error) {
	var podGroups []PodGroup
	query := db.Model(&PodGroup{}).Select("id, name, pod_cluster_id, pod_namespace_id")
	if err := query.Find(&podGroups).Error; err != nil {
		return nil, err
	}
	sql := query.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Find(&podGroups)
	})
	logger.Printf("获取工作负载查询 sql: %s", sql)
	logger.Printf("获取 %d 条工作负载", len(podGroups))
	return podGroups, nil
}

type vmInfo struct {
	names  mapset.Set[string]
	vpcIds mapset.Set[int]
}

type podGroupInfo struct {
	names           mapset.Set[string]
	podClusterIds   mapset.Set[int]
	podNamespaceIds mapset.Set[int]
}

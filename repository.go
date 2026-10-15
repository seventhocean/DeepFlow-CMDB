package main

import (
	"gorm.io/gorm"
)

func newRepository(db *gorm.DB) *Repository {
	return &Repository{
		db: db,
	}
}

type Repository struct {
	db *gorm.DB
}

func (r *Repository) Update(item BizService) error {
	logger.Printf("更新业务服务: %s", item.Name)
	if cfg.LogDebug {
		logger.Printf("更新业务服务: %+v", item)
	}
	return r.db.Save(&item).Error
}

func (r *Repository) Add(items []BizService) error {
	logger.Printf("开始创建 %d 条业务服务", len(items))
	if cfg.LogDebug {
		logger.Printf("创建业务服务:")
		for _, item := range items {
			logger.Printf("  %+v", item)
		}
	}

	count := len(items)
	offset := 1000
	pages := count/offset + 1
	if count%offset == 0 {
		pages = count / offset
	}
	for i := 0; i < pages; i++ {
		start := i * offset
		end := (i + 1) * offset
		if end > count {
			end = count
		}
		p := items[start:end]
		err := r.db.Create(&p).Error
		if err != nil {
			return err
		}
	}
	logger.Printf("创建 %d 条业务服务完成", len(items))
	return nil
}

func (r *Repository) Delete(items []BizService) error {
	var deletedItems []cmdbDataItem
	for _, item := range items {
		deletedItems = append(deletedItems, cmdbDataItem{
			appid: item.Name,
			name:  item.Resources,
		})
	}
	logger.Printf("开始删除 %d 条业务服务", len(items))
	if cfg.LogDebug {
		logger.Printf("删除业务服务:")
		for _, item := range items {
			logger.Printf("  %+v", item)
		}
	}

	count := len(items)
	offset := 1000
	pages := count/offset + 1
	if count%offset == 0 {
		pages = count / offset
	}
	for i := 0; i < pages; i++ {
		start := i * offset
		end := (i + 1) * offset
		if end > count {
			end = count
		}
		p := items[start:end]
		err := r.db.Delete(&p).Error
		if err != nil {
			return err
		}
	}

	logger.Printf("删除 %d 条业务服务完成", len(items))
	return nil
}

package main

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Base struct {
	ID     int    `gorm:"primaryKey;autoIncrement;unique;column:id;type:int;not null" json:"ID" mapstructure:"ID"`
	Lcuuid string `gorm:"unique;column:lcuuid;type:char(64)" json:"LCUUID" mapstructure:"LCUUID"`
}

func (b Base) GetID() int {
	return b.ID
}

func (b *Base) SetID(id int) {
	b.ID = id
}

func (b Base) GetLcuuid() string {
	return b.Lcuuid
}

type OperatedTime struct {
	CreatedAt time.Time `gorm:"autoCreateTime;column:created_at;type:datetime" json:"CREATED_AT" mapstructure:"CREATED_AT"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;column:updated_at;type:datetime" json:"UPDATED_AT" mapstructure:"UPDATED_AT"`
}

func (t *OperatedTime) SetUpdatedAt(updatedAt time.Time) {
	t.UpdatedAt = updatedAt
}

func (t OperatedTime) GetUpdatedAt() time.Time {
	return t.UpdatedAt
}

type SoftDeleteBase struct {
	OperatedTime `mapstructure:",squash"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;type:datetime;default:null" json:"DELETED_AT" mapstructure:"DELETED_AT"`
}

type BizService struct {
	Base             `gorm:"embedded" mapstructure:",squash"`
	CreatedAt        LocalTime                                  `gorm:"autoCreateTime;column:created_at;type:datetime" json:"CREATED_AT" mapstructure:"CREATED_AT"`
	UpdatedAt        LocalTime                                  `gorm:"autoUpdateTime;column:updated_at;type:datetime" json:"UPDATED_AT" mapstructure:"UPDATED_AT"`
	Name             string                                     `gorm:"column:name;type:varchar(128);default:''" json:"NAME" mapstructure:"NAME"`
	Type             int                                        `gorm:"column:type;type:int;default:0" json:"TYPE" mapstructure:"TYPE"`
	MatchType        int                                        `gorm:"column:match_type;type:int;default:0" json:"MATCH_TYPE" mapstructure:"MATCH_TYPE"`
	VPCIDs           AutoSplitedInts                            `gorm:"column:epc_ids;type:text;default:''" json:"EPC_IDS" mapstructure:"EPC_IDS"`
	PodClusterIDs    AutoSplitedInts                            `gorm:"column:pod_cluster_ids;type:text;default:''" json:"POD_CLUSTER_IDS" mapstructure:"POD_CLUSTER_IDS"`
	PodNamespaceIDs  AutoSplitedInts                            `gorm:"column:pod_namespace_ids;type:text;default:''" json:"POD_NAMESPACE_IDS" mapstructure:"POD_NAMESPACE_IDS"`
	Resources        string                                     `gorm:"column:resources;type:text;default:''" json:"RESOURCES" mapstructure:"RESOURCES"`
	ResourcesContext datatypes.JSONType[map[string]interface{}] `gorm:"column:resources_context;type:json;default:'{}'" json:"RESOURCES_CONTEXT" mapstructure:"RESOURCES_CONTEXT"`
	TeamID           int                                        `gorm:"column:team_id;type:int;default:0" json:"TEAM_ID" mapstructure:"TEAM_ID"`
	UserID           int                                        `gorm:"column:user_id;type:int;default:1" json:"USER_ID"`
	ServiceGroupName string                                     `gorm:"column:service_group_name;type:varchar(128);default:''" json:"SERVICE_GROUP_NAME" mapstructure:"SERVICE_GROUP_NAME"`
	SystemID         int                                        `gorm:"column:system_id;type:int;not null" json:"SYSTEM_ID" mapstructure:"SYSTEM_ID"`
	Tags             datatypes.JSONType[map[string]string]      `gorm:"column:tags;type:json;default:'{}'" json:"TAGS" mapstructure:"TAGS"`
}

type BizSystem struct {
	Base      `gorm:"embedded" mapstructure:",squash"`
	CreatedAt LocalTime `gorm:"autoCreateTime;column:created_at;type:datetime" json:"CREATED_AT" mapstructure:"CREATED_AT"`
	UpdatedAt LocalTime `gorm:"autoUpdateTime;column:updated_at;type:datetime" json:"UPDATED_AT" mapstructure:"UPDATED_AT"`
	Name      string    `gorm:"column:name;type:varchar(255);not null" json:"NAME" mapstructure:"NAME"`
	TeamID    int       `gorm:"column:team_id;type:int;default:0" json:"TEAM_ID" mapstructure:"TEAM_ID"`
	UserID    int       `gorm:"column:user_id;type:int;default:1" json:"USER_ID"`
}

type VM struct {
	Base             `gorm:"embedded" mapstructure:",squash"`
	SoftDeleteBase   `gorm:"embedded" mapstructure:",squash"`
	State            int               `gorm:"index:state_server_index;column:state;type:int;not null" json:"STATE" mapstructure:"STATE"` // 0.Temp 1.Creating 2.Created 3.To run 4.Running 5.To suspend 6.Suspended 7.To resume 8. To stop 9.Stopped 10.Modifing 11.Exception 12.Destroying
	Name             string            `gorm:"column:name;type:varchar(256);default:''" json:"NAME" mapstructure:"NAME"`
	Label            string            `gorm:"column:label;type:char(64);default:''" json:"LABEL" mapstructure:"LABEL"`
	IP               string            `gorm:"column:ip;type:char(64);default:''" json:"IP" mapstructure:"IP"`
	Hostname         string            `gorm:"column:hostname;type:char(64);default:''" json:"HOSTNAME" mapstructure:"HOSTNAME"`
	CreateMethod     int               `gorm:"column:create_method;type:int;default:0" json:"CREATE_METHOD" mapstructure:"CREATE_METHOD"` // 0.learning 1.user_defined
	HType            int               `gorm:"column:htype;type:int;default:1" json:"HTYPE" mapstructure:"HTYPE"`                         // 1.vm-c 2.bm-c 3.vm-n 4.bm-n 5.vm-s 6.bm-s
	LaunchServer     string            `gorm:"index:state_server_index;column:launch_server;type:char(64);default:''" json:"LAUNCH_SERVER" mapstructure:"LAUNCH_SERVER"`
	HostID           int               `gorm:"column:host_id;type:int;default:0" json:"HOST_ID" mapstructure:"HOST_ID"`
	VPCID            int               `gorm:"column:epc_id;type:int;default:0" json:"EPC_ID" mapstructure:"EPC_ID"`
	Domain           string            `gorm:"column:domain;type:char(64);not null" json:"DOMAIN" mapstructure:"DOMAIN"`
	AZ               string            `gorm:"column:az;type:char(64);default:''" json:"AZ" mapstructure:"AZ"`
	Region           string            `gorm:"column:region;type:char(64);default:''" json:"REGION" mapstructure:"REGION"`
	UID              string            `gorm:"column:uid;type:char(64);default:''" json:"UID" mapstructure:"UID"`
	LearnedCloudTags map[string]string `gorm:"column:learned_cloud_tags;type:text;default:'';serializer:json" json:"LEARNED_CLOUD_TAGS" mapstructure:"LEARNED_CLOUD_TAGS"`
	CustomCloudTags  map[string]string `gorm:"column:custom_cloud_tags;type:text;default:'';serializer:json" json:"CUSTOM_CLOUD_TAGS" mapstructure:"CUSTOM_CLOUD_TAGS"`
	NetworkID        int               `gorm:"column:vl2id;type:int;default:null" json:"VL2ID" mapstructure:"VL2ID"`
}

func (VM) TableName() string {
	return "vm"
}

type PodGroup struct {
	Base           `gorm:"embedded" mapstructure:",squash"`
	SoftDeleteBase `gorm:"embedded" mapstructure:",squash"`
	Name           string              `gorm:"column:name;type:varchar(256);default:''" json:"NAME" mapstructure:"NAME"`
	Alias          string              `gorm:"column:alias;type:char(64);default:''" json:"ALIAS" mapstructure:"ALIAS"`
	Type           int                 `gorm:"column:type;type:int;default:null" json:"TYPE" mapstructure:"TYPE"` // 1: Deployment 2: StatefulSet 3: ReplicationController
	PodNum         int                 `gorm:"column:pod_num;type:int;default:1" json:"POD_NUM" mapstructure:"POD_NUM"`
	Label          string              `gorm:"column:label;type:text;default:''" json:"LABEL" mapstructure:"LABEL"`                    // separated by ,
	NetworkMode    int                 `gorm:"column:network_mode;type:int;default:1" json:"NETWORK_MODE" mapstructure:"NETWORK_MODE"` // 1: Pod network 2: Host network
	Metadata       AutoCompressedBytes `gorm:"column:compressed_metadata;type:mediumblob" json:"METADATA" mapstructure:"-"`
	MetadataHash   string              `gorm:"column:metadata_hash;type:char(64);default:''" json:"METADATA_HASH" mapstructure:"-"`
	Spec           AutoCompressedBytes `gorm:"column:compressed_spec;type:mediumblob" json:"SPEC" mapstructure:"-"`
	SpecHash       string              `gorm:"column:spec_hash;type:char(64);default:''" json:"SPEC_HASH" mapstructure:"-"`
	PodNamespaceID int                 `gorm:"column:pod_namespace_id;type:int;default:null" json:"POD_NAMESPACE_ID" mapstructure:"POD_NAMESPACE_ID"`
	PodClusterID   int                 `gorm:"column:pod_cluster_id;type:int;default:null" json:"POD_CLUSTER_ID" mapstructure:"POD_CLUSTER_ID"`
	AZ             string              `gorm:"column:az;type:char(64);default:''" json:"AZ" mapstructure:"AZ"`
	Region         string              `gorm:"column:region;type:char(64);default:''" json:"REGION" mapstructure:"REGION"`
	SubDomain      string              `gorm:"column:sub_domain;type:char(64);default:''" json:"SUB_DOMAIN" mapstructure:"SUB_DOMAIN"`
	Domain         string              `gorm:"column:domain;type:char(64);not null" json:"DOMAIN" mapstructure:"DOMAIN"`
	UID            string              `gorm:"column:uid;type:char(64);default:''" json:"UID" mapstructure:"UID"`
}

type cmdbDataItem struct {
	appid string
	name  string
}

type dfVmKey struct {
	vpcId int
	name  string
}

type dfPodGroupKey struct {
	podClusterId   int
	podNamespaceId int
	name           string
}

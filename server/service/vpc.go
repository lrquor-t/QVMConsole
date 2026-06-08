package service

import (
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/utils"
)

const (
	vpcConfigDir                         = "/etc/kvm-console/vpc"
	vpcSwitchTrafficPenaltyMbps          = 1
	defaultVPCSwitchName                 = "默认交换机"
	autoPortForwardSecurityGroupRuleNote = "端口转发自动放行"
)

type VPCSwitchRequest struct {
	Username          string  `json:"username"`
	Name              string  `json:"name"`
	BridgeName        string  `json:"bridge_name"`
	BridgeVLANID      int     `json:"bridge_vlan_id"`
	AllowPromiscuous  bool    `json:"allow_promiscuous"`
	AllowMACChange    bool    `json:"allow_mac_change"`
	AllowForgedTx     bool    `json:"allow_forged_transmits"`
	TrafficDownGB     float64 `json:"traffic_down_gb"`
	TrafficUpGB       float64 `json:"traffic_up_gb"`
	BandwidthMbps     int     `json:"bandwidth_mbps"` // 兼容旧版字段，传入时同时作为上下行默认值
	BandwidthDownMbps int     `json:"bandwidth_down_mbps"`
	BandwidthUpMbps   int     `json:"bandwidth_up_mbps"`
}

type VPCSecurityGroupRequest struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Remark   string `json:"remark"`
}

type VPCSecurityGroupRuleRequest struct {
	Direction   string `json:"direction"`
	Protocol    string `json:"protocol"`
	PortStart   int    `json:"port_start"`
	PortEnd     int    `json:"port_end"`
	TargetType  string `json:"target_type"`
	TargetValue string `json:"target_value"`
	Remark      string `json:"remark"`
}

type VPCQuotaInfo struct {
	Username               string  `json:"username"`
	MaxTrafficDown         float64 `json:"max_traffic_down"`
	MaxTrafficUp           float64 `json:"max_traffic_up"`
	AllocatedTrafficDown   float64 `json:"allocated_traffic_down"`
	AllocatedTrafficUp     float64 `json:"allocated_traffic_up"`
	RemainingTrafficDown   float64 `json:"remaining_traffic_down"`
	RemainingTrafficUp     float64 `json:"remaining_traffic_up"`
	MaxBandwidthDown       float64 `json:"max_bandwidth_down"`
	MaxBandwidthUp         float64 `json:"max_bandwidth_up"`
	AllocatedBandwidthDown float64 `json:"allocated_bandwidth_down"`
	AllocatedBandwidthUp   float64 `json:"allocated_bandwidth_up"`
	RemainingBandwidthDown float64 `json:"remaining_bandwidth_down"`
	RemainingBandwidthUp   float64 `json:"remaining_bandwidth_up"`
}

type VPCBindingInfo struct {
	Binding          *model.VPCVMBinding       `json:"binding"`
	Bindings         []model.VPCVMBinding      `json:"bindings,omitempty"`
	Switch           *model.VPCSwitch          `json:"switch"`
	SecurityGroup    *model.VPCSecurityGroup   `json:"security_group"`
	Groups           []model.VPCSecurityGroup  `json:"groups"`
	Switches         []model.VPCSwitch         `json:"switches"`
	LightweightQuota *model.LightweightVMQuota `json:"lightweight_quota,omitempty"`
}

func currentTrafficMonth() string {
	return time.Now().Format("2006-01")
}

func normalizeVPCName(value string) string {
	value = strings.TrimSpace(value)
	value = regexp.MustCompile(`\s+`).ReplaceAllString(value, "-")
	return value
}

func normalizeVPCSwitchBandwidthRequest(req *VPCSwitchRequest) {
	if req == nil {
		return
	}
	if req.BandwidthDownMbps <= 0 && req.BandwidthUpMbps <= 0 && req.BandwidthMbps > 0 {
		req.BandwidthDownMbps = req.BandwidthMbps
		req.BandwidthUpMbps = req.BandwidthMbps
	}
	if req.BandwidthDownMbps < 0 {
		req.BandwidthDownMbps = 0
	}
	if req.BandwidthUpMbps < 0 {
		req.BandwidthUpMbps = 0
	}
	if req.BandwidthMbps <= 0 && req.BandwidthDownMbps == req.BandwidthUpMbps {
		req.BandwidthMbps = req.BandwidthDownMbps
	}
}

func normalizeVPCSwitchBandwidthForResponse(sw *model.VPCSwitch) {
	if sw == nil {
		return
	}
	if sw.BandwidthDownMbps <= 0 && sw.BandwidthUpMbps <= 0 && sw.BandwidthMbps > 0 {
		sw.BandwidthDownMbps = sw.BandwidthMbps
		sw.BandwidthUpMbps = sw.BandwidthMbps
	}
}

func fillVPCSwitchUsageForResponse(sw *model.VPCSwitch) {
	if sw == nil {
		return
	}
	normalizeVPCSwitchBandwidthForResponse(sw)
	down, up := AggregateSwitchMonthlyTraffic(sw.ID)
	sw.UsedTrafficDown = down
	sw.UsedTrafficUp = up
	sw.UsedTrafficDownGB = formatTrafficBytes(down)
	sw.UsedTrafficUpGB = formatTrafficBytes(up)
	sw.IsLimitedDown, sw.IsLimitedUp = IsVPCSwitchTrafficLimited(sw.ID)
	sw.EffectiveBandwidthDownMbps, sw.EffectiveBandwidthUpMbps = effectiveVPCSwitchBandwidth(*sw)
}

func resolveVPCUsername(operator, role, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if role == "admin" && requested != "" {
		return requested, nil
	}
	if strings.TrimSpace(operator) == "" {
		return "", fmt.Errorf("无法识别当前用户")
	}
	return operator, nil
}

// EnsureDefaultSecurityGroup 确保用户存在默认安全组。
func EnsureDefaultSecurityGroup(username string) (*model.VPCSecurityGroup, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("用户名不能为空")
	}
	var group model.VPCSecurityGroup
	if err := model.DB.Where("username = ? AND is_default = ?", username, true).First(&group).Error; err == nil {
		return &group, nil
	}
	group = model.VPCSecurityGroup{
		Username:  username,
		Name:      "默认安全组",
		IsDefault: true,
		Remark:    "系统自动创建，默认拒绝入站、允许出站",
	}
	if err := model.DB.Create(&group).Error; err != nil {
		return nil, fmt.Errorf("创建默认安全组失败: %w", err)
	}
	return &group, nil
}

func EnsureDefaultVPCSwitch(username string) (*model.VPCSwitch, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("用户名不能为空")
	}
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, fmt.Errorf("用户不存在")
	}
	if user.Role != "user" {
		return nil, nil
	}
	var sw model.VPCSwitch
	if err := model.DB.Where("username = ?", username).Order("id ASC").First(&sw).Error; err == nil {
		return &sw, nil
	}
	req := defaultVPCSwitchRequestForUser(user)
	created, err := CreateVPCSwitch("system", "admin", req)
	if err != nil && created != nil {
		fmt.Printf("[警告] 默认交换机 %s 已创建，但运行态应用失败: %v\n", created.Name, err)
		return created, nil
	}
	return created, err
}

func defaultVPCSwitchRequestForUser(user model.User) VPCSwitchRequest {
	return VPCSwitchRequest{
		Username:          user.Username,
		Name:              defaultVPCSwitchName,
		TrafficDownGB:     defaultSwitchTrafficQuota(user.MaxTrafficDown),
		TrafficUpGB:       defaultSwitchTrafficQuota(user.MaxTrafficUp),
		BandwidthDownMbps: defaultSwitchBandwidthQuota(user.MaxBandwidthDown),
		BandwidthUpMbps:   defaultSwitchBandwidthQuota(user.MaxBandwidthUp),
	}
}

func defaultSwitchTrafficQuota(max float64) float64 {
	if max > 0 {
		return max
	}
	return 0
}

func defaultSwitchBandwidthQuota(max float64) int {
	if max <= 0 {
		return 0
	}
	value := int(max)
	if value <= 0 {
		return 1
	}
	return value
}

func EnsureAllActiveUsersDefaultSecurityGroup() {
	var users []model.User
	model.DB.Where("role = ? AND status = ?", "user", UserStatusActive).Find(&users)
	for _, user := range users {
		if _, err := EnsureDefaultSecurityGroup(user.Username); err != nil {
			fmt.Printf("[警告] 为用户 %s 创建默认安全组失败: %v\n", user.Username, err)
		}
	}
}

func GetVPCQuota(username string) (*VPCQuotaInfo, error) {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, fmt.Errorf("用户不存在")
	}
	var switches []model.VPCSwitch
	model.DB.Where("username = ?", username).Find(&switches)
	info := &VPCQuotaInfo{
		Username:         username,
		MaxTrafficDown:   user.MaxTrafficDown,
		MaxTrafficUp:     user.MaxTrafficUp,
		MaxBandwidthDown: user.MaxBandwidthDown,
		MaxBandwidthUp:   user.MaxBandwidthUp,
	}
	for _, sw := range switches {
		info.AllocatedTrafficDown += sw.TrafficDownGB
		info.AllocatedTrafficUp += sw.TrafficUpGB
		info.AllocatedBandwidthDown += float64(sw.BandwidthDownMbps)
		info.AllocatedBandwidthUp += float64(sw.BandwidthUpMbps)
	}
	if user.MaxTrafficDown > 0 {
		info.RemainingTrafficDown = user.MaxTrafficDown - info.AllocatedTrafficDown
	}
	if user.MaxTrafficUp > 0 {
		info.RemainingTrafficUp = user.MaxTrafficUp - info.AllocatedTrafficUp
	}
	if user.MaxBandwidthDown > 0 {
		info.RemainingBandwidthDown = user.MaxBandwidthDown - info.AllocatedBandwidthDown
	}
	if user.MaxBandwidthUp > 0 {
		info.RemainingBandwidthUp = user.MaxBandwidthUp - info.AllocatedBandwidthUp
	}
	if info.RemainingTrafficDown < 0 {
		info.RemainingTrafficDown = 0
	}
	if info.RemainingTrafficUp < 0 {
		info.RemainingTrafficUp = 0
	}
	if info.RemainingBandwidthDown < 0 {
		info.RemainingBandwidthDown = 0
	}
	if info.RemainingBandwidthUp < 0 {
		info.RemainingBandwidthUp = 0
	}
	return info, nil
}

func ListVPCSwitches(operator, role, requestedUsername string) ([]model.VPCSwitch, error) {
	query := model.DB.Model(&model.VPCSwitch{})
	if role != "admin" {
		query = query.Where("username = ? AND (bridge_mode = '' OR bridge_mode = ? OR bridge_mode IS NULL)", operator, BridgeModeNAT)
	} else if strings.TrimSpace(requestedUsername) != "" {
		query = query.Where("username = ?", strings.TrimSpace(requestedUsername))
	}
	var switches []model.VPCSwitch
	if err := query.Order("username ASC, id ASC").Find(&switches).Error; err != nil {
		return nil, err
	}
	for i := range switches {
		fillVPCSwitchUsageForResponse(&switches[i])
	}
	return switches, nil
}

func CreateVPCSwitch(operator, role string, req VPCSwitchRequest) (*model.VPCSwitch, error) {
	username, err := resolveVPCUsername(operator, role, req.Username)
	if err != nil {
		return nil, err
	}
	bridgeName, bridgeMode, err := resolveVPCSwitchBridge(role, req.BridgeName)
	if err != nil {
		return nil, err
	}
	if err := validateBridgeVLANID(bridgeMode, req.BridgeVLANID); err != nil {
		return nil, err
	}
	if _, err := EnsureDefaultSecurityGroup(username); err != nil {
		return nil, err
	}
	req.Name = normalizeVPCName(req.Name)
	if req.Name == "" {
		return nil, fmt.Errorf("交换机名称不能为空")
	}
	normalizeVPCSwitchBandwidthRequest(&req)
	if err := checkSwitchResourceQuota(username, 0, req); err != nil {
		return nil, err
	}
	var count int64
	model.DB.Model(&model.VPCSwitch{}).Where("username = ? AND name = ?", username, req.Name).Count(&count)
	if count > 0 {
		return nil, fmt.Errorf("交换机名称已存在")
	}
	vlanID, err := allocateVPCVLANID()
	if err != nil {
		return nil, err
	}
	cidr, gateway, dhcpStart, dhcpEnd, err := allocateVPCSubnet()
	if err != nil {
		return nil, err
	}
	sw := &model.VPCSwitch{
		Username:             username,
		Name:                 req.Name,
		BridgeName:           bridgeName,
		BridgeMode:           bridgeMode,
		BridgeVLANID:         normalizedBridgeVLANID(bridgeMode, req.BridgeVLANID),
		AllowPromiscuous:     bridgeMode == BridgeModeDirect && req.AllowPromiscuous,
		AllowMACChange:       bridgeMode == BridgeModeDirect && req.AllowMACChange,
		AllowForgedTransmits: bridgeMode == BridgeModeDirect && req.AllowForgedTx,
		VLANID:               vlanID,
		CIDR:                 cidr,
		GatewayIP:            gateway,
		DHCPStart:            dhcpStart,
		DHCPEnd:              dhcpEnd,
		TrafficDownGB:        req.TrafficDownGB,
		TrafficUpGB:          req.TrafficUpGB,
		BandwidthMbps:        req.BandwidthMbps,
		BandwidthDownMbps:    req.BandwidthDownMbps,
		BandwidthUpMbps:      req.BandwidthUpMbps,
	}
	if err := model.DB.Create(sw).Error; err != nil {
		return nil, fmt.Errorf("创建交换机失败: %w", err)
	}
	if err := EnsureVPCSwitchRuntime(*sw); err != nil {
		return sw, err
	}
	return sw, nil
}

func UpdateVPCSwitch(operator, role string, id uint, req VPCSwitchRequest) (*model.VPCSwitch, error) {
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, id).Error; err != nil {
		return nil, fmt.Errorf("交换机不存在")
	}
	if role != "admin" && sw.Username != operator {
		return nil, fmt.Errorf("无权操作此交换机")
	}
	if req.BridgeName != "" && req.BridgeName != sw.BridgeName {
		return nil, fmt.Errorf("暂不支持修改交换机目标网桥")
	}
	if err := validateBridgeVLANID(BridgeModeForSwitch(sw), req.BridgeVLANID); err != nil {
		return nil, err
	}
	if req.Name = normalizeVPCName(req.Name); req.Name != "" {
		sw.Name = req.Name
	}
	sw.BridgeVLANID = normalizedBridgeVLANID(BridgeModeForSwitch(sw), req.BridgeVLANID)
	if SwitchUsesDirectBridge(sw) {
		sw.AllowPromiscuous = req.AllowPromiscuous
		sw.AllowMACChange = req.AllowMACChange
		sw.AllowForgedTransmits = req.AllowForgedTx
	} else {
		sw.AllowPromiscuous = false
		sw.AllowMACChange = false
		sw.AllowForgedTransmits = false
	}
	sw.TrafficDownGB = req.TrafficDownGB
	sw.TrafficUpGB = req.TrafficUpGB
	normalizeVPCSwitchBandwidthRequest(&req)
	sw.BandwidthMbps = req.BandwidthMbps
	sw.BandwidthDownMbps = req.BandwidthDownMbps
	sw.BandwidthUpMbps = req.BandwidthUpMbps
	if err := checkSwitchResourceQuota(sw.Username, sw.ID, req); err != nil {
		return nil, err
	}
	if err := model.DB.Save(&sw).Error; err != nil {
		return nil, err
	}
	if SwitchUsesDirectBridge(sw) {
		for _, vmName := range listVPCSwitchVMNames(sw) {
			if err := ApplyVPCSwitchRuntime(vmName, sw); err != nil {
				return nil, err
			}
		}
	}
	CheckVPCSwitchTrafficAfterQuotaUpdate(sw.ID)
	_ = EnsureVPCSwitchRuntime(sw)
	fillVPCSwitchUsageForResponse(&sw)
	return &sw, nil
}

func resolveVPCSwitchBridge(role, requested string) (string, string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == ovsBridgeName() {
		return ovsBridgeName(), BridgeModeNAT, nil
	}
	if role != "admin" {
		return "", "", fmt.Errorf("仅管理员可创建桥接直通交换机")
	}
	var bridge model.NetworkBridge
	if err := model.DB.Where("name = ? AND mode = ?", requested, BridgeModeDirect).First(&bridge).Error; err != nil {
		return "", "", fmt.Errorf("桥接网桥不存在")
	}
	return bridge.Name, BridgeModeDirect, nil
}

func validateBridgeVLANID(bridgeMode string, vlanID int) error {
	if normalizeBridgeMode(bridgeMode) != BridgeModeDirect {
		return nil
	}
	if vlanID < 0 || vlanID > 4094 {
		return fmt.Errorf("桥接 VLAN ID 必须为 0-4094，0 表示不打 VLAN")
	}
	return nil
}

func normalizedBridgeVLANID(bridgeMode string, vlanID int) int {
	if normalizeBridgeMode(bridgeMode) != BridgeModeDirect {
		return 0
	}
	if vlanID < 0 || vlanID > 4094 {
		return 0
	}
	return vlanID
}

func ResetVPCSwitchTraffic(operator, role string, id uint) error {
	if role != "admin" {
		return fmt.Errorf("仅管理员可重置交换机流量计数器")
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, id).Error; err != nil {
		return fmt.Errorf("交换机不存在")
	}
	rawDown, rawUp := aggregateSwitchMonthlyTrafficRaw(id)
	record := getOrCreateVPCSwitchTrafficMonthly(sw, currentTrafficMonth())
	record.OffsetDown = rawDown
	record.OffsetUp = rawUp
	record.TrafficDown = 0
	record.TrafficUp = 0
	record.IsLimitedDown = false
	record.IsLimitedUp = false
	if err := saveVPCSwitchTrafficMonthly(record); err != nil {
		return err
	}
	if err := applyVPCSwitchBandwidth(sw); err != nil {
		return fmt.Errorf("解除交换机限速失败: %w", err)
	}
	log.Printf("[VPC 流量配额] 管理员 %s 已重置交换机 %s(%d) 流量计数器", operator, sw.Name, sw.ID)
	return nil
}

func DeleteVPCSwitch(operator, role string, id uint) error {
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, id).Error; err != nil {
		return fmt.Errorf("交换机不存在")
	}
	if role != "admin" && sw.Username != operator {
		return fmt.Errorf("无权操作此交换机")
	}
	var count int64
	model.DB.Model(&model.VPCVMBinding{}).Where("switch_id = ?", id).Count(&count)
	if count > 0 {
		return fmt.Errorf("交换机仍有虚拟机绑定，不能删除")
	}
	if err := model.DB.Delete(&sw).Error; err != nil {
		return err
	}
	_ = removeVPCSwitchRuntime(sw)
	return nil
}

func CleanupUserNetworkResources(username string, vmNames []string) error {
	username = strings.TrimSpace(username)
	if username == "" || model.DB == nil {
		return nil
	}
	cleanupOVSStaticHostsForVMs(vmNames)

	var switches []model.VPCSwitch
	if err := model.DB.Where("username = ?", username).Find(&switches).Error; err != nil {
		return fmt.Errorf("查询用户 VPC 交换机失败: %w", err)
	}
	for _, sw := range switches {
		removePortForwardsForCIDR(sw.CIDR)
		if err := removeVPCSwitchRuntime(sw); err != nil {
			fmt.Printf("[警告] 清理用户 %s 的 VPC 交换机 %s(%d) 运行态失败: %v\n", username, sw.Name, sw.ID, err)
		}
	}

	var groups []model.VPCSecurityGroup
	if err := model.DB.Where("username = ?", username).Find(&groups).Error; err != nil {
		return fmt.Errorf("查询用户安全组失败: %w", err)
	}
	groupIDs := make([]uint, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
	}
	if len(groupIDs) > 0 {
		if err := model.DB.Where("security_group_id IN ?", groupIDs).Delete(&model.VPCSecurityGroupRule{}).Error; err != nil {
			return fmt.Errorf("删除用户安全组规则失败: %w", err)
		}
	}
	if err := model.DB.Where("username = ?", username).Delete(&model.VPCVMBinding{}).Error; err != nil {
		return fmt.Errorf("删除用户 VPC VM 绑定失败: %w", err)
	}
	if err := model.DB.Where("username = ?", username).Delete(&model.VPCSwitchTrafficMonthly{}).Error; err != nil {
		return fmt.Errorf("删除用户 VPC 交换机流量记录失败: %w", err)
	}
	if err := model.DB.Where("username = ?", username).Delete(&model.VPCSecurityGroup{}).Error; err != nil {
		return fmt.Errorf("删除用户安全组失败: %w", err)
	}
	if err := model.DB.Where("username = ?", username).Delete(&model.VPCSwitch{}).Error; err != nil {
		return fmt.Errorf("删除用户 VPC 交换机失败: %w", err)
	}

	if len(switches) > 0 || len(groups) > 0 {
		if err := ApplyVPCACLRules(); err != nil {
			fmt.Printf("[警告] 清理用户 %s 后重建 VPC ACL 失败: %v\n", username, err)
		}
		if err := SavePortForwardRules(); err != nil {
			fmt.Printf("[警告] 清理用户 %s 后保存端口转发规则失败: %v\n", username, err)
		}
	}
	return nil
}

func CleanupVMVPCBinding(vmName string) {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || model.DB == nil {
		return
	}
	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&binding).Error; err != nil {
		return
	}
	switchID := binding.SwitchID
	if err := model.DB.Delete(&binding).Error; err != nil {
		fmt.Printf("[警告] 清理 VM %s 的 VPC 绑定失败: %v\n", vmName, err)
		return
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err == nil {
		if err := applyVPCSwitchBandwidth(sw); err != nil {
			fmt.Printf("[警告] 清理 VM %s 后刷新交换机 %s(%d) 带宽失败: %v\n", vmName, sw.Name, sw.ID, err)
		}
	}
	if err := ApplyVPCACLRules(); err != nil {
		fmt.Printf("[警告] 清理 VM %s 后重建 VPC ACL 失败: %v\n", vmName, err)
	}
}

func checkSwitchResourceQuota(username string, excludeID uint, req VPCSwitchRequest) error {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在")
	}
	if err := validateSwitchDirectionTrafficQuota("下行", user.MaxTrafficDown, req.TrafficDownGB); err != nil {
		return err
	}
	if err := validateSwitchDirectionTrafficQuota("上行", user.MaxTrafficUp, req.TrafficUpGB); err != nil {
		return err
	}
	if err := validateSwitchDirectionBandwidthQuota("下行", user.MaxBandwidthDown, req.BandwidthDownMbps); err != nil {
		return err
	}
	if err := validateSwitchDirectionBandwidthQuota("上行", user.MaxBandwidthUp, req.BandwidthUpMbps); err != nil {
		return err
	}
	var switches []model.VPCSwitch
	model.DB.Where("username = ?", username).Find(&switches)
	totalDown := req.TrafficDownGB
	totalUp := req.TrafficUpGB
	totalBandwidthDown := float64(req.BandwidthDownMbps)
	totalBandwidthUp := float64(req.BandwidthUpMbps)
	for _, sw := range switches {
		if sw.ID == excludeID {
			continue
		}
		if user.MaxBandwidthDown > 0 && sw.BandwidthDownMbps <= 0 {
			return fmt.Errorf("已有交换机 %s 的下行总带宽为不限，请先调整后再继续", sw.Name)
		}
		if user.MaxBandwidthUp > 0 && sw.BandwidthUpMbps <= 0 {
			return fmt.Errorf("已有交换机 %s 的上行总带宽为不限，请先调整后再继续", sw.Name)
		}
		totalDown += sw.TrafficDownGB
		totalUp += sw.TrafficUpGB
		totalBandwidthDown += float64(sw.BandwidthDownMbps)
		totalBandwidthUp += float64(sw.BandwidthUpMbps)
	}
	if user.MaxTrafficDown > 0 && totalDown > user.MaxTrafficDown {
		return fmt.Errorf("下行月流量配额不足：交换机合计 %.2fGB / 用户上限 %.2fGB", totalDown, user.MaxTrafficDown)
	}
	if user.MaxTrafficUp > 0 && totalUp > user.MaxTrafficUp {
		return fmt.Errorf("上行月流量配额不足：交换机合计 %.2fGB / 用户上限 %.2fGB", totalUp, user.MaxTrafficUp)
	}
	if user.MaxBandwidthDown > 0 && totalBandwidthDown > user.MaxBandwidthDown {
		return fmt.Errorf("下行总带宽配额不足：交换机合计 %.2fMbps / 用户上限 %.2fMbps", totalBandwidthDown, user.MaxBandwidthDown)
	}
	if user.MaxBandwidthUp > 0 && totalBandwidthUp > user.MaxBandwidthUp {
		return fmt.Errorf("上行总带宽配额不足：交换机合计 %.2fMbps / 用户上限 %.2fMbps", totalBandwidthUp, user.MaxBandwidthUp)
	}
	return nil
}

func validateSwitchDirectionTrafficQuota(label string, userMax, switchValue float64) error {
	if switchValue < 0 {
		return fmt.Errorf("交换机%s月流量配额不能小于 0", label)
	}
	if userMax > 0 && switchValue <= 0 {
		return fmt.Errorf("用户%s月流量配额有限，交换机%s月流量配额必须大于 0；只有用户该方向配额不限时才能设置为 0", label, label)
	}
	return nil
}

func validateSwitchDirectionBandwidthQuota(label string, userMax float64, switchValue int) error {
	if switchValue < 0 {
		return fmt.Errorf("交换机%s总带宽不能小于 0", label)
	}
	if userMax > 0 && switchValue <= 0 {
		return fmt.Errorf("用户%s带宽配额有限，交换机%s总带宽必须大于 0；只有用户该方向带宽不限时才能设置为 0", label, label)
	}
	return nil
}

func allocateVPCVLANID() (int, error) {
	start, end := config.GlobalConfig.VPCVLANStart, config.GlobalConfig.VPCVLANEnd
	if start <= 0 {
		start = 100
	}
	if end < start {
		end = 4094
	}
	var switches []model.VPCSwitch
	model.DB.Find(&switches)
	used := map[int]bool{}
	for _, sw := range switches {
		used[sw.VLANID] = true
	}
	for id := start; id <= end; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, fmt.Errorf("VLAN 范围 %d-%d 内没有可用 ID", start, end)
}

func allocateVPCSubnet() (cidr, gateway, dhcpStart, dhcpEnd string, err error) {
	prefix := strings.Trim(config.GlobalConfig.VPCSubnetPrefix, ". ")
	if prefix == "" {
		prefix = "10.200"
	}
	var switches []model.VPCSwitch
	model.DB.Find(&switches)
	used := map[string]bool{}
	for _, sw := range switches {
		used[sw.CIDR] = true
	}
	for i := 1; i <= 254; i++ {
		base := fmt.Sprintf("%s.%d", prefix, i)
		candidate := base + ".0/24"
		if !used[candidate] {
			return candidate, base + ".1", base + ".10", base + ".250", nil
		}
	}
	return "", "", "", "", fmt.Errorf("VPC 子网池 %s.1-254 已用尽", prefix)
}

func EnsureVPCSwitchRuntime(sw model.VPCSwitch) error {
	if strings.TrimSpace(sw.BridgeName) == "" {
		sw.BridgeName = ovsBridgeName()
	}
	if strings.TrimSpace(sw.BridgeMode) == "" {
		sw.BridgeMode = BridgeModeNAT
	}
	if SwitchUsesDirectBridge(sw) {
		var bridge model.NetworkBridge
		if err := model.DB.Where("name = ?", BridgeNameForSwitch(sw)).First(&bridge).Error; err != nil {
			return fmt.Errorf("桥接网桥 %s 不存在", BridgeNameForSwitch(sw))
		}
		if err := EnsureOVSBridgeDirect(bridge.Name, bridge.UplinkIF, bridge.MigrateHostIP); err != nil {
			return err
		}
		return applyVPCSwitchBandwidth(sw)
	}
	if err := EnsureOVSNetworkReady(); err != nil {
		return err
	}
	if err := os.MkdirAll(vpcConfigDir, 0755); err != nil {
		return fmt.Errorf("创建 VPC 配置目录失败: %w", err)
	}
	bridge := ovsBridgeName()
	port := vpcGatewayPortName(sw.ID)
	if result := utils.ExecCommand("ovs-vsctl", "--may-exist", "add-port", bridge, port, "tag="+strconv.Itoa(sw.VLANID), "--", "set", "Interface", port, "type=internal"); result.Error != nil {
		return fmt.Errorf("创建 VPC 网关端口失败: %s", result.Stderr)
	}
	utils.ExecCommand("ip", "link", "set", port, "up")
	utils.ExecShell(fmt.Sprintf("ip -4 addr show dev %s | grep -q '%s/24' || ip addr add %s/24 dev %s",
		utils.ShellSingleQuote(port), utils.ShellSingleQuote(sw.GatewayIP), utils.ShellSingleQuote(sw.GatewayIP), utils.ShellSingleQuote(port)))
	if err := ensureLocalDNSMasqInputRules(port); err != nil {
		return err
	}
	if err := ensureVPCSwitchNAT(sw, port); err != nil {
		return err
	}
	if _, err := os.Stat(vpcDHCPHostsPath(sw.ID)); os.IsNotExist(err) {
		if err := os.WriteFile(vpcDHCPHostsPath(sw.ID), []byte(""), 0644); err != nil {
			return fmt.Errorf("创建 VPC 静态 DHCP 绑定文件失败: %w", err)
		}
	}
	configChanged, err := writeVPCDNSMasqConfig(sw, port)
	if err != nil {
		return err
	}
	ensureVPCDNSMasq(sw.ID, configChanged)
	if err := applyVPCSwitchBandwidth(sw); err != nil {
		return err
	}
	return nil
}

func EnsureAllVPCSwitchRuntime() error {
	if model.DB == nil {
		return nil
	}
	var switches []model.VPCSwitch
	if err := model.DB.Order("id ASC").Find(&switches).Error; err != nil {
		return err
	}
	var lastErr error
	for _, sw := range switches {
		if err := EnsureVPCSwitchRuntime(sw); err != nil {
			lastErr = err
			fmt.Printf("[警告] 恢复 VPC 交换机 %s(%d) 运行态失败: %v\n", sw.Name, sw.ID, err)
		}
	}
	if err := applyAllVPCBindingsRuntime(false); err != nil {
		lastErr = err
		fmt.Printf("[警告] 恢复 VPC VM 绑定运行态失败: %v\n", err)
	}
	if len(switches) > 0 {
		if err := ApplyVPCACLRules(); err != nil {
			lastErr = err
			fmt.Printf("[警告] 恢复 VPC ACL 失败: %v\n", err)
		}
	}
	return lastErr
}

func ensureVPCSwitchNAT(sw model.VPCSwitch, gatewayPort string) error {
	uplink := ovsUplink()
	if uplink == "" {
		return fmt.Errorf("无法检测 VPC NAT 出口网卡，请配置 KVM_OVS_UPLINK")
	}
	cleanupStaleManagedNATRules(sw.CIDR, gatewayPort, uplink)
	if err := ensureIPTablesRule(
		fmt.Sprintf("iptables -t nat -C POSTROUTING -s %s -o %s -j MASQUERADE", utils.ShellSingleQuote(sw.CIDR), utils.ShellSingleQuote(uplink)),
		fmt.Sprintf("iptables -t nat -A POSTROUTING -s %s -o %s -j MASQUERADE", utils.ShellSingleQuote(sw.CIDR), utils.ShellSingleQuote(uplink)),
		"配置 VPC 出站 NAT",
	); err != nil {
		return err
	}
	if err := ensureIPTablesRule(
		fmt.Sprintf("iptables -C FORWARD -i %s -o %s -j ACCEPT", utils.ShellSingleQuote(gatewayPort), utils.ShellSingleQuote(uplink)),
		fmt.Sprintf("iptables -A FORWARD -i %s -o %s -j ACCEPT", utils.ShellSingleQuote(gatewayPort), utils.ShellSingleQuote(uplink)),
		"配置 VPC 出站转发",
	); err != nil {
		return err
	}
	if err := ensureIPTablesRule(
		fmt.Sprintf("iptables -C FORWARD -i %s -o %s -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", utils.ShellSingleQuote(uplink), utils.ShellSingleQuote(gatewayPort)),
		fmt.Sprintf("iptables -A FORWARD -i %s -o %s -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", utils.ShellSingleQuote(uplink), utils.ShellSingleQuote(gatewayPort)),
		"配置 VPC 回程转发",
	); err != nil {
		return err
	}
	return nil
}

func removeVPCSwitchNAT(sw model.VPCSwitch) {
	port := vpcGatewayPortName(sw.ID)
	removeLocalDNSMasqInputRules(port)
	uplink := ovsUplink()
	if uplink == "" {
		return
	}
	utils.ExecShell(fmt.Sprintf("while iptables -t nat -D POSTROUTING -s %s -o %s -j MASQUERADE 2>/dev/null; do :; done",
		utils.ShellSingleQuote(sw.CIDR), utils.ShellSingleQuote(uplink)))
	utils.ExecShell(fmt.Sprintf("while iptables -D FORWARD -i %s -o %s -j ACCEPT 2>/dev/null; do :; done",
		utils.ShellSingleQuote(port), utils.ShellSingleQuote(uplink)))
	utils.ExecShell(fmt.Sprintf("while iptables -D FORWARD -i %s -o %s -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null; do :; done",
		utils.ShellSingleQuote(uplink), utils.ShellSingleQuote(port)))
}

func vpcSwitchCookie(switchID uint) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("kvm-console-vpc-switch:%d", switchID)))
	return fmt.Sprintf("0x%x", h.Sum64())
}

func vpcSwitchMeterID(switchID uint, direction string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("kvm-console-vpc-switch:%d:%s", switchID, direction)))
	return 200000 + h.Sum32()%800000000
}

func clearVPCSwitchBandwidth(sw model.VPCSwitch) {
	bridge := BridgeNameForSwitch(sw)
	cookie := vpcSwitchCookie(sw.ID)
	utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "del-flows", bridge, "cookie="+cookie+"/0xffffffffffffffff")
	utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "del-meter", bridge, ovsBandwidthMeterArg(vpcSwitchMeterID(sw.ID, "down")))
	utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "del-meter", bridge, ovsBandwidthMeterArg(vpcSwitchMeterID(sw.ID, "up")))
	if !SwitchUsesDirectBridge(sw) {
		clearTCVPCSwitchDownlinkLimit(vpcGatewayPortName(sw.ID))
	}
}

var (
	vpcSwitchBandwidthMu   sync.Mutex
	vpcSwitchBandwidthLocks = make(map[uint]*sync.Mutex)
)

func lockVPCSwitchBandwidth(switchID uint) func() {
	vpcSwitchBandwidthMu.Lock()
	mu, ok := vpcSwitchBandwidthLocks[switchID]
	if !ok {
		mu = &sync.Mutex{}
		vpcSwitchBandwidthLocks[switchID] = mu
	}
	vpcSwitchBandwidthMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func applyVPCSwitchBandwidth(sw model.VPCSwitch) error {
	unlock := lockVPCSwitchBandwidth(sw.ID)
	defer unlock()

	normalizeVPCSwitchBandwidthForResponse(&sw)
	downMbps, upMbps := effectiveVPCSwitchBandwidth(sw)
	downRateKbit := downMbps * 1000
	upRateKbit := upMbps * 1000
	if downRateKbit <= 0 && upRateKbit <= 0 {
		clearVPCSwitchBandwidth(sw)
		return nil
	}
	bridge := BridgeNameForSwitch(sw)
	gatewayOfport := ""
	if downRateKbit > 0 && !SwitchUsesDirectBridge(sw) {
		gatewayOfport = getOVSInterfaceOfPort(vpcGatewayPortName(sw.ID))
		if gatewayOfport == "" {
			return fmt.Errorf("无法获取 VPC 交换机 %s 的网关端口号", sw.Name)
		}
	}
	vmOfports := []string{}
	if upRateKbit > 0 {
		vmOfports = listVPCSwitchVMOfports(sw.ID)
	}
	clearVPCSwitchBandwidth(sw)
	downMeter := vpcSwitchMeterID(sw.ID, "down")
	upMeter := vpcSwitchMeterID(sw.ID, "up")
	if downRateKbit > 0 {
		if SwitchUsesDirectBridge(sw) {
			if err := addOVSBandwidthMeter(bridge, downMeter, downRateKbit); err != nil {
				return err
			}
		} else {
			applyTCVPCSwitchDownlinkLimit(vpcGatewayPortName(sw.ID), downMbps)
		}
	}
	if upRateKbit > 0 {
		if err := addOVSBandwidthMeter(bridge, upMeter, upRateKbit); err != nil {
			return err
		}
	}
	var flows []string
	var directVMPorts []vpcSwitchVMPortMatch
	if SwitchUsesDirectBridge(sw) {
		directVMPorts = listVPCSwitchVMPortMatches(sw)
		flows = buildDirectBridgeSwitchBandwidthFlows(sw, directVMPorts, downMeter, upMeter, downRateKbit, upRateKbit)
	} else {
		flows = buildVPCSwitchBandwidthFlows(sw, gatewayOfport, vmOfports, upMeter, downRateKbit, upRateKbit)
	}
	for _, flow := range flows {
		result := utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "add-flow", bridge, flow)
		if result.Error != nil {
			return fmt.Errorf("配置 VPC 交换机总带宽失败: %s", result.Stderr)
		}
	}
	if SwitchUsesDirectBridge(sw) {
		if err := applyDirectBridgePortSecurity(bridge, directVMPorts, sw.AllowPromiscuous); err != nil {
			return err
		}
	}
	return nil
}

type vpcSwitchVMPortMatch struct {
	PortName string
	OFPort   string
	MAC      string
}

func listVPCSwitchVMOfports(switchID uint) []string {
	if model.DB == nil {
		return nil
	}
	var bindings []model.VPCVMBinding
	model.DB.Where("switch_id = ?", switchID).Order("vm_name ASC").Find(&bindings)
	seen := map[string]bool{}
	ofports := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		vnetIF := getVMVnetIF(binding.VMName)
		if vnetIF == "" {
			continue
		}
		ofport := getOVSInterfaceOfPort(vnetIF)
		if ofport == "" || seen[ofport] {
			continue
		}
		seen[ofport] = true
		ofports = append(ofports, ofport)
	}
	sort.Strings(ofports)
	return ofports
}

func listVPCSwitchVMPortMatches(sw model.VPCSwitch) []vpcSwitchVMPortMatch {
	if model.DB == nil {
		return nil
	}
	seen := map[string]bool{}
	var matches []vpcSwitchVMPortMatch
	for _, vmName := range listVPCSwitchVMNames(sw) {
		vnetIF := getVMVnetIF(vmName)
		mac := strings.ToLower(strings.TrimSpace(getFirstVMMAC(vmName)))
		ofport := getOVSInterfaceOfPort(vnetIF)
		if ofport == "" || mac == "" || seen[ofport+"/"+mac] {
			continue
		}
		seen[ofport+"/"+mac] = true
		matches = append(matches, vpcSwitchVMPortMatch{PortName: vnetIF, OFPort: ofport, MAC: mac})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].OFPort == matches[j].OFPort {
			return matches[i].MAC < matches[j].MAC
		}
		return matches[i].OFPort < matches[j].OFPort
	})
	return matches
}

func listVPCSwitchVMNames(sw model.VPCSwitch) []string {
	seen := map[string]bool{}
	var names []string
	var bindings []model.VPCVMBinding
	model.DB.Where("switch_id = ?", sw.ID).Order("vm_name ASC").Find(&bindings)
	for _, binding := range bindings {
		name := strings.TrimSpace(binding.VMName)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if SwitchUsesDirectBridge(sw) && directBridgeSwitchCount(BridgeNameForSwitch(sw)) == 1 {
		for _, name := range listDirectBridgeVMNames(BridgeNameForSwitch(sw)) {
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func directBridgeSwitchCount(bridgeName string) int64 {
	var count int64
	if strings.TrimSpace(bridgeName) == "" || model.DB == nil {
		return 0
	}
	model.DB.Model(&model.VPCSwitch{}).Where("bridge_mode = ? AND bridge_name = ?", BridgeModeDirect, bridgeName).Count(&count)
	return count
}

func listDirectBridgeVMNames(bridgeName string) []string {
	bridgeName = strings.TrimSpace(bridgeName)
	if bridgeName == "" {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, vmName := range listAllVMNames() {
		if vmUsesOVSBridge(vmName, bridgeName) && !seen[vmName] {
			seen[vmName] = true
			names = append(names, vmName)
		}
	}
	sort.Strings(names)
	return names
}

func vmUsesOVSBridge(vmName, bridgeName string) bool {
	for _, iface := range parseVirshDomiflistOutput(utils.ExecCommand("virsh", "domiflist", vmName).Stdout) {
		if iface.Type == "bridge" && iface.Source == bridgeName {
			return true
		}
	}
	for _, args := range [][]string{{"dumpxml", vmName, "--inactive"}, {"dumpxml", vmName}} {
		result := utils.ExecCommand("virsh", args...)
		if result.Error == nil && firstOVSInterfaceUsesBridge(result.Stdout, bridgeName) {
			return true
		}
	}
	return false
}

func buildVPCSwitchBandwidthFlows(sw model.VPCSwitch, gatewayOfport string, vmOfports []string, upMeter uint32, downRateKbit, upRateKbit int) []string {
	cookie := vpcSwitchCookie(sw.ID)
	if upRateKbit > 0 {
		sort.Strings(vmOfports)
	}
	flows := []string{}
	if upRateKbit > 0 {
		for _, vmOfport := range vmOfports {
			if strings.TrimSpace(vmOfport) == "" {
				continue
			}
			flows = append(flows,
				fmt.Sprintf("cookie=%s,priority=90,in_port=%s,ip,nw_src=%s,nw_dst=%s,actions=NORMAL", cookie, vmOfport, sw.CIDR, sw.CIDR),
				fmt.Sprintf("cookie=%s,priority=80,in_port=%s,ip,nw_src=%s,actions=meter:%d,NORMAL", cookie, vmOfport, sw.CIDR, upMeter),
			)
		}
	}
	if downRateKbit > 0 {
		flows = append(flows, fmt.Sprintf("cookie=%s,priority=80,in_port=%s,ip,nw_dst=%s,actions=NORMAL", cookie, gatewayOfport, sw.CIDR))
	}
	return flows
}

func buildDirectBridgeSwitchBandwidthFlows(sw model.VPCSwitch, vmPorts []vpcSwitchVMPortMatch, downMeter, upMeter uint32, downRateKbit, upRateKbit int) []string {
	cookie := vpcSwitchCookie(sw.ID)
	var flows []string
	restrictSourceMAC := !sw.AllowMACChange || !sw.AllowForgedTransmits
	for _, item := range vmPorts {
		if strings.TrimSpace(item.OFPort) != "" {
			if restrictSourceMAC && strings.TrimSpace(item.MAC) != "" {
				action := "NORMAL"
				if upRateKbit > 0 {
					action = fmt.Sprintf("meter:%d,NORMAL", upMeter)
				}
				flows = append(flows, fmt.Sprintf("cookie=%s,priority=90,in_port=%s,dl_src=%s,actions=%s", cookie, item.OFPort, item.MAC, action))
				flows = append(flows, fmt.Sprintf("cookie=%s,priority=85,in_port=%s,actions=drop", cookie, item.OFPort))
			} else if upRateKbit > 0 {
				flows = append(flows, fmt.Sprintf("cookie=%s,priority=80,in_port=%s,actions=meter:%d,NORMAL", cookie, item.OFPort, upMeter))
			}
		}
		if downRateKbit > 0 && strings.TrimSpace(item.MAC) != "" {
			flows = append(flows, fmt.Sprintf("cookie=%s,priority=80,dl_dst=%s,actions=meter:%d,NORMAL", cookie, item.MAC, downMeter))
		}
	}
	return flows
}

func applyDirectBridgePortSecurity(bridge string, vmPorts []vpcSwitchVMPortMatch, allowPromiscuous bool) error {
	if strings.TrimSpace(bridge) == "" {
		return nil
	}
	mode := "no-flood"
	if allowPromiscuous {
		mode = "flood"
	}
	for _, item := range vmPorts {
		if strings.TrimSpace(item.PortName) == "" {
			continue
		}
		result := utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "mod-port", bridge, item.PortName, mode)
		if result.Error != nil {
			return fmt.Errorf("配置桥接端口混杂模式策略失败: %s", result.Stderr)
		}
	}
	return nil
}

func removeVPCSwitchRuntime(sw model.VPCSwitch) error {
	clearVPCSwitchBandwidth(sw)
	if SwitchUsesDirectBridge(sw) {
		return nil
	}
	removeVPCSwitchNAT(sw)
	stopVPCDNSMasq(sw.ID)
	utils.ExecCommand("ovs-vsctl", "--if-exists", "del-port", ovsBridgeName(), vpcGatewayPortName(sw.ID))
	_ = os.Remove(vpcDNSMasqConfigPath(sw.ID))
	_ = os.Remove(vpcDHCPHostsPath(sw.ID))
	_ = os.Remove(vpcDHCPLeasesPath(sw.ID))
	return nil
}

func vpcGatewayPortName(id uint) string {
	return fmt.Sprintf("vpcsw%d", id)
}

func vpcDNSMasqConfigPath(id uint) string {
	return filepath.Join(vpcConfigDir, fmt.Sprintf("dnsmasq-%d.conf", id))
}

func vpcDNSMasqPIDPath(id uint) string {
	return filepath.Join(vpcConfigDir, fmt.Sprintf("dnsmasq-%d.pid", id))
}

func vpcDHCPHostsPath(id uint) string {
	return filepath.Join(vpcConfigDir, fmt.Sprintf("dhcp-hosts-%d", id))
}

func vpcDHCPLeasesPath(id uint) string {
	return filepath.Join(vpcConfigDir, fmt.Sprintf("leases-%d", id))
}

func writeVPCDNSMasqConfig(sw model.VPCSwitch, iface string) (bool, error) {
	content := fmt.Sprintf(`interface=%s
bind-interfaces
except-interface=lo
dhcp-authoritative
dhcp-range=%s,%s,255.255.255.0,12h
dhcp-option=option:router,%s
dhcp-option=option:dns-server,%s
dhcp-hostsfile=%s
pid-file=%s
dhcp-leasefile=%s
log-dhcp
`, iface, sw.DHCPStart, sw.DHCPEnd, sw.GatewayIP, config.GlobalConfig.VPCDNS, vpcDHCPHostsPath(sw.ID), vpcDNSMasqPIDPath(sw.ID), vpcDHCPLeasesPath(sw.ID))
	changed, err := writeFileIfChanged(vpcDNSMasqConfigPath(sw.ID), []byte(content), 0644)
	if err != nil {
		return false, fmt.Errorf("写入 VPC DHCP 配置失败: %w", err)
	}
	return changed, nil
}

func startVPCDNSMasq(id uint) {
	stopVPCDNSMasq(id)
	utils.ExecCommand("dnsmasq", "--conf-file="+vpcDNSMasqConfigPath(id))
}

func ensureVPCDNSMasq(id uint, configChanged bool) {
	if configChanged {
		startVPCDNSMasq(id)
		return
	}
	if !isVPCDNSMasqRunning(id) {
		startVPCDNSMasq(id)
	}
}

func isVPCDNSMasqRunning(id uint) bool {
	pidPath := vpcDNSMasqPIDPath(id)
	result := utils.ExecShell(fmt.Sprintf("[ -f %s ] && ps -p $(cat %s) -o comm= | grep -q '^dnsmasq$'",
		utils.ShellSingleQuote(pidPath), utils.ShellSingleQuote(pidPath)))
	return result.Error == nil
}

func reloadVPCDNSMasq(id uint) {
	pidPath := vpcDNSMasqPIDPath(id)
	result := utils.ExecShell(fmt.Sprintf("[ -f %s ] && kill -HUP $(cat %s)", utils.ShellSingleQuote(pidPath), utils.ShellSingleQuote(pidPath)))
	if result.Error == nil {
		return
	}
	if _, err := os.Stat(vpcDNSMasqConfigPath(id)); err == nil {
		startVPCDNSMasq(id)
	}
}

func stopVPCDNSMasq(id uint) {
	pidPath := vpcDNSMasqPIDPath(id)
	utils.ExecShell(fmt.Sprintf("[ -f %s ] && kill $(cat %s) 2>/dev/null || true", utils.ShellSingleQuote(pidPath), utils.ShellSingleQuote(pidPath)))
	_ = os.Remove(pidPath)
}

func ListVPCSecurityGroups(operator, role, requestedUsername string) ([]model.VPCSecurityGroup, error) {
	if role != "admin" && IsLightweightCloudUser(operator) {
		vmNames := GetUserVMList(operator)
		if len(vmNames) == 0 {
			return []model.VPCSecurityGroup{}, nil
		}
		var groups []model.VPCSecurityGroup
		if err := model.DB.Preload("Rules").
			Where("is_vm_scoped = ? AND vm_name IN ?", true, vmNames).
			Order("vm_name ASC, id ASC").
			Find(&groups).Error; err != nil {
			return nil, err
		}
		return groups, nil
	}
	if role != "admin" {
		if _, err := EnsureDefaultSecurityGroup(operator); err != nil {
			return nil, err
		}
	} else if strings.TrimSpace(requestedUsername) != "" {
		_, _ = EnsureDefaultSecurityGroup(strings.TrimSpace(requestedUsername))
	}
	cleanupInvalidVPCSecurityGroupRules(operator, role, requestedUsername)
	query := model.DB.Preload("Rules").Model(&model.VPCSecurityGroup{})
	if role != "admin" {
		query = query.Where("username = ?", operator)
	} else if strings.TrimSpace(requestedUsername) != "" {
		query = query.Where("username = ?", strings.TrimSpace(requestedUsername))
	}
	var groups []model.VPCSecurityGroup
	if err := query.Order("username ASC, is_default DESC, id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	return groups, nil
}

func cleanupInvalidVPCSecurityGroupRules(operator, role, requestedUsername string) {
	if model.DB == nil {
		return
	}
	groupQuery := model.DB.Model(&model.VPCSecurityGroup{})
	if role != "admin" {
		groupQuery = groupQuery.Where("username = ?", operator)
	} else if strings.TrimSpace(requestedUsername) != "" {
		groupQuery = groupQuery.Where("username = ?", strings.TrimSpace(requestedUsername))
	}
	var groups []model.VPCSecurityGroup
	if err := groupQuery.Find(&groups).Error; err != nil || len(groups) == 0 {
		return
	}
	groupUsernames := map[uint]string{}
	groupIDs := make([]uint, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
		groupUsernames[group.ID] = group.Username
	}
	var rules []model.VPCSecurityGroupRule
	if err := model.DB.Where("security_group_id IN ? AND target_type IN ?", groupIDs, []string{"switch", "security_group"}).Find(&rules).Error; err != nil {
		return
	}
	for _, rule := range rules {
		username := groupUsernames[rule.SecurityGroupID]
		if err := validateSecurityGroupRuleTarget(username, rule.TargetType, rule.TargetValue); err != nil {
			fmt.Printf("[VPC ACL] 清理异常安全组规则 %d: %v\n", rule.ID, err)
			_ = model.DB.Delete(&rule).Error
		}
	}
}

func CreateVPCSecurityGroup(operator, role string, req VPCSecurityGroupRequest) (*model.VPCSecurityGroup, error) {
	if role != "admin" && IsLightweightCloudUser(operator) {
		return nil, fmt.Errorf("轻量云用户不能创建全局安全组")
	}
	username, err := resolveVPCUsername(operator, role, req.Username)
	if err != nil {
		return nil, err
	}
	req.Name = normalizeVPCName(req.Name)
	if req.Name == "" {
		return nil, fmt.Errorf("安全组名称不能为空")
	}
	var count int64
	model.DB.Model(&model.VPCSecurityGroup{}).Where("username = ? AND name = ?", username, req.Name).Count(&count)
	if count > 0 {
		return nil, fmt.Errorf("安全组名称已存在")
	}
	group := &model.VPCSecurityGroup{Username: username, Name: req.Name, Remark: strings.TrimSpace(req.Remark)}
	if err := model.DB.Create(group).Error; err != nil {
		return nil, err
	}
	return group, nil
}

func UpdateVPCSecurityGroup(operator, role string, id uint, req VPCSecurityGroupRequest) (*model.VPCSecurityGroup, error) {
	if role != "admin" && IsLightweightCloudUser(operator) {
		return nil, fmt.Errorf("轻量云用户不能修改安全组")
	}
	var group model.VPCSecurityGroup
	if err := model.DB.First(&group, id).Error; err != nil {
		return nil, fmt.Errorf("安全组不存在")
	}
	if role != "admin" && group.Username != operator {
		return nil, fmt.Errorf("无权操作此安全组")
	}
	nextName := group.Name
	if strings.TrimSpace(req.Name) != "" {
		nextName = normalizeVPCName(req.Name)
		if nextName == "" {
			return nil, fmt.Errorf("安全组名称不能为空")
		}
	}
	if group.IsDefault && nextName != group.Name {
		return nil, fmt.Errorf("默认安全组不能修改名称")
	}
	if nextName != group.Name {
		var count int64
		model.DB.Model(&model.VPCSecurityGroup{}).
			Where("username = ? AND name = ? AND id <> ?", group.Username, nextName, group.ID).
			Count(&count)
		if count > 0 {
			return nil, fmt.Errorf("安全组名称已存在")
		}
		group.Name = nextName
	}
	group.Remark = strings.TrimSpace(req.Remark)
	if err := model.DB.Save(&group).Error; err != nil {
		return nil, err
	}
	return &group, nil
}

func DeleteVPCSecurityGroup(operator, role string, id uint) error {
	var group model.VPCSecurityGroup
	if err := model.DB.First(&group, id).Error; err != nil {
		return fmt.Errorf("安全组不存在")
	}
	if role != "admin" && IsLightweightCloudUser(operator) {
		return fmt.Errorf("轻量云用户不能删除安全组")
	}
	if role != "admin" && group.Username != operator {
		return fmt.Errorf("无权操作此安全组")
	}
	if group.IsDefault {
		return fmt.Errorf("默认安全组不能删除")
	}
	var count int64
	model.DB.Model(&model.VPCVMBinding{}).Where("security_group_id = ?", id).Count(&count)
	if count > 0 {
		return fmt.Errorf("安全组仍被虚拟机使用，不能删除")
	}
	model.DB.Where("security_group_id = ?", id).Delete(&model.VPCSecurityGroupRule{})
	return model.DB.Delete(&group).Error
}

func AddVPCSecurityGroupRule(operator, role string, groupID uint, req VPCSecurityGroupRuleRequest) (*model.VPCSecurityGroupRule, error) {
	var group model.VPCSecurityGroup
	if err := model.DB.First(&group, groupID).Error; err != nil {
		return nil, fmt.Errorf("安全组不存在")
	}
	if role != "admin" && group.Username != operator && !(group.IsVMScoped && UserOwnsVM(operator, group.VMName)) {
		return nil, fmt.Errorf("无权操作此安全组")
	}
	if role != "admin" && IsLightweightCloudUser(operator) {
		targetType := strings.ToLower(strings.TrimSpace(req.TargetType))
		if targetType == "" {
			targetType = "cidr"
		}
		if targetType != "cidr" {
			return nil, fmt.Errorf("轻量云安全组规则仅支持 CIDR 目标")
		}
	}
	rule, err := normalizeSecurityGroupRule(groupID, req)
	if err != nil {
		return nil, err
	}
	if err := validateSecurityGroupRuleTarget(group.Username, rule.TargetType, rule.TargetValue); err != nil {
		return nil, err
	}
	if err := model.DB.Create(rule).Error; err != nil {
		return nil, err
	}
	return rule, nil
}

func DeleteVPCSecurityGroupRule(operator, role string, ruleID uint) error {
	var rule model.VPCSecurityGroupRule
	if err := model.DB.First(&rule, ruleID).Error; err != nil {
		return fmt.Errorf("安全组规则不存在")
	}
	var group model.VPCSecurityGroup
	if err := model.DB.First(&group, rule.SecurityGroupID).Error; err != nil {
		if role == "admin" {
			return model.DB.Delete(&rule).Error
		}
		return fmt.Errorf("安全组不存在")
	}
	if role != "admin" && group.Username != operator && !(group.IsVMScoped && UserOwnsVM(operator, group.VMName)) {
		return fmt.Errorf("无权操作此安全组规则")
	}
	return model.DB.Delete(&rule).Error
}

func normalizeSecurityGroupRule(groupID uint, req VPCSecurityGroupRuleRequest) (*model.VPCSecurityGroupRule, error) {
	direction := strings.ToLower(strings.TrimSpace(req.Direction))
	if direction == "" {
		direction = "ingress"
	}
	if direction != "ingress" && direction != "egress" {
		return nil, fmt.Errorf("方向只支持 ingress 或 egress")
	}
	proto := strings.ToLower(strings.TrimSpace(req.Protocol))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" && proto != "icmp" && proto != "all" {
		return nil, fmt.Errorf("协议只支持 tcp/udp/icmp/all")
	}
	targetType := strings.ToLower(strings.TrimSpace(req.TargetType))
	if targetType == "" {
		targetType = "cidr"
	}
	if targetType != "cidr" && targetType != "switch" && targetType != "security_group" {
		return nil, fmt.Errorf("目标类型无效")
	}
	targetValue := strings.TrimSpace(req.TargetValue)
	if targetValue == "" {
		if targetType == "cidr" {
			targetValue = "0.0.0.0/0"
		} else {
			return nil, fmt.Errorf("请选择目标交换机或安全组")
		}
	}
	if targetType == "cidr" {
		if _, err := netip.ParsePrefix(normalizeCIDROrIP(targetValue)); err != nil {
			return nil, fmt.Errorf("CIDR 无效: %s", targetValue)
		}
		targetValue = normalizeCIDROrIP(targetValue)
	}
	if req.PortEnd == 0 {
		req.PortEnd = req.PortStart
	}
	if (proto == "tcp" || proto == "udp") && (req.PortStart < 1 || req.PortStart > 65535 || req.PortEnd < req.PortStart || req.PortEnd > 65535) {
		return nil, fmt.Errorf("端口范围无效")
	}
	if proto == "icmp" || proto == "all" {
		req.PortStart = 0
		req.PortEnd = 0
	}
	return &model.VPCSecurityGroupRule{
		SecurityGroupID: groupID,
		Direction:       direction,
		Protocol:        proto,
		PortStart:       req.PortStart,
		PortEnd:         req.PortEnd,
		TargetType:      targetType,
		TargetValue:     targetValue,
		Remark:          strings.TrimSpace(req.Remark),
	}, nil
}

func validateSecurityGroupRuleTarget(username, targetType, targetValue string) error {
	switch targetType {
	case "switch":
		id, err := strconv.Atoi(strings.TrimSpace(targetValue))
		if err != nil || id <= 0 {
			return fmt.Errorf("请选择有效的目标交换机")
		}
		var count int64
		model.DB.Model(&model.VPCSwitch{}).Where("id = ? AND username = ?", id, username).Count(&count)
		if count == 0 {
			return fmt.Errorf("目标交换机不存在或不属于该用户")
		}
	case "security_group":
		id, err := strconv.Atoi(strings.TrimSpace(targetValue))
		if err != nil || id <= 0 {
			return fmt.Errorf("请选择有效的目标安全组")
		}
		var count int64
		model.DB.Model(&model.VPCSecurityGroup{}).Where("id = ? AND username = ?", id, username).Count(&count)
		if count == 0 {
			return fmt.Errorf("目标安全组不存在或不属于该用户")
		}
	}
	return nil
}

func normalizeCIDROrIP(value string) string {
	value = strings.TrimSpace(value)
	if addr, err := netip.ParseAddr(value); err == nil && addr.Is4() {
		return addr.String() + "/32"
	}
	return value
}

func BindVMToVPC(username, vmName string, switchID, securityGroupID uint) error {
	if strings.TrimSpace(vmName) == "" {
		return fmt.Errorf("虚拟机名称不能为空")
	}
	if username == "" {
		username = FindVMOwner(vmName)
	}
	if username == "" && switchID > 0 {
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, switchID).Error; err == nil {
			username = sw.Username
		}
	}
	if username == "" {
		return fmt.Errorf("无法识别虚拟机归属用户")
	}
	if _, err := EnsureDefaultSecurityGroup(username); err != nil {
		return err
	}
	var sw model.VPCSwitch
	if err := model.DB.Where("id = ? AND username = ?", switchID, username).First(&sw).Error; err != nil {
		return fmt.Errorf("交换机不存在或不属于该用户")
	}
	if err := validateVMCanApplyVPCSwitch(vmName, sw); err != nil {
		return err
	}
	if SwitchUsesDirectBridge(sw) {
		if securityGroupID != 0 {
			var sg model.VPCSecurityGroup
			if err := model.DB.Where("id = ? AND username = ?", securityGroupID, username).First(&sg).Error; err != nil {
				return fmt.Errorf("安全组不存在或不属于该用户")
			}
		}
	} else {
		var sg model.VPCSecurityGroup
		if err := model.DB.Where("id = ? AND username = ?", securityGroupID, username).First(&sg).Error; err != nil {
			return fmt.Errorf("安全组不存在或不属于该用户")
		}
	}
	var oldSwitchID uint
	var oldTrafficDown, oldTrafficUp int64
	newTrafficDown, newTrafficUp := AggregateSwitchMonthlyTraffic(switchID)
	binding := model.VPCVMBinding{
		VMName:          vmName,
		Username:        username,
		SwitchID:        switchID,
		SecurityGroupID: securityGroupID,
	}
	var existing model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&existing).Error; err == nil {
		if existing.SwitchID != switchID {
			oldSwitchID = existing.SwitchID
			oldTrafficDown, oldTrafficUp = AggregateSwitchMonthlyTraffic(oldSwitchID)
			if mac := getFirstVMMAC(vmName); mac != "" {
				_, _ = RemoveVPCStaticHost(existing.SwitchID, vmName, mac)
			}
		}
		existing.Username = username
		existing.SwitchID = switchID
		existing.SecurityGroupID = securityGroupID
		if err := model.DB.Save(&existing).Error; err != nil {
			return err
		}
	} else if err := model.DB.Create(&binding).Error; err != nil {
		return err
	}
	if oldSwitchID != 0 {
		rebaseVPCSwitchTrafficMonthly(oldSwitchID, oldTrafficDown, oldTrafficUp)
	}
	rebaseVPCSwitchTrafficMonthly(switchID, newTrafficDown, newTrafficUp)
	// VM 绑定 VPC 后默认由交换机控制聚合带宽，清理旧的 VM 级 OVS 限速流表。
	if err := ClearVMBandwidth(vmName); err != nil {
		fmt.Printf("[警告] 清理 VM %s 单机速率限制失败: %v\n", vmName, err)
	}
	if err := ApplyVPCSwitchRuntime(vmName, sw); err != nil {
		return err
	}
	if SwitchUsesDirectBridge(sw) {
		return nil
	}
	return ApplyVPCACLRules()
}

func BindVMToVPCAsAdmin(vmName string, switchID, securityGroupID uint) error {
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return fmt.Errorf("交换机不存在")
	}
	if _, err := EnsureDefaultSecurityGroup(sw.Username); err != nil {
		return err
	}
	if SwitchUsesDirectBridge(sw) {
		return BindVMToVPC(sw.Username, vmName, switchID, 0)
	}
	if securityGroupID == 0 {
		var group model.VPCSecurityGroup
		if err := model.DB.Where("username = ? AND is_default = ?", sw.Username, true).First(&group).Error; err != nil {
			return fmt.Errorf("未找到交换机用户 %s 的默认安全组", sw.Username)
		}
		securityGroupID = group.ID
	} else {
		var group model.VPCSecurityGroup
		if err := model.DB.First(&group, securityGroupID).Error; err != nil {
			return fmt.Errorf("安全组不存在")
		}
		if group.Username != sw.Username {
			return fmt.Errorf("安全组必须属于交换机用户 %s", sw.Username)
		}
	}
	return BindVMToVPC(sw.Username, vmName, switchID, securityGroupID)
}

func ApplyVPCSwitchToDomainXML(vmXML string, switchID uint) (string, error) {
	if switchID == 0 {
		return vmXML, nil
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return "", fmt.Errorf("VPC 交换机不存在: %w", err)
	}
	if err := EnsureVPCSwitchRuntime(sw); err != nil {
		return "", err
	}
	if SwitchUsesDirectBridge(sw) {
		updated, changed := setFirstOVSInterfaceDirectBridge(vmXML, BridgeNameForSwitch(sw), sw.BridgeVLANID)
		if !changed {
			return "", fmt.Errorf("无法在虚拟机 XML 中找到可接入桥接网桥的 OVS 网卡")
		}
		return updated, nil
	}
	updated, changed := setFirstOVSInterfaceVPC(vmXML, sw.VLANID)
	if !changed {
		return "", fmt.Errorf("无法在虚拟机 XML 中找到可接入 VPC 的 OVS 网卡")
	}
	return updated, nil
}

func GetVPCBindingInfo(operator, role, vmName string) (*VPCBindingInfo, error) {
	if role != "admin" && !UserOwnsVM(operator, vmName) {
		return nil, fmt.Errorf("无权操作此虚拟机")
	}
	isLightweightOperator := role != "admin" && IsLightweightCloudUser(operator)
	username := FindVMOwner(vmName)
	if username == "" && role != "admin" {
		username = operator
	}
	if username != "" && !isLightweightOperator {
		_, _ = EnsureDefaultSecurityGroup(username)
	}
	info := &VPCBindingInfo{}
	var allBindings []model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).Order("interface_order ASC").Find(&allBindings).Error; err == nil && len(allBindings) > 0 {
		info.Bindings = allBindings
		// 第一个绑定作为主绑定（向后兼容）
		binding := allBindings[0]
		info.Binding = &binding
		var sw model.VPCSwitch
		if model.DB.First(&sw, binding.SwitchID).Error == nil {
			normalizeVPCSwitchBandwidthForResponse(&sw)
			info.Switch = &sw
		}
		var sg model.VPCSecurityGroup
		if model.DB.Preload("Rules").First(&sg, binding.SecurityGroupID).Error == nil {
			info.SecurityGroup = &sg
		}
		if username == "" {
			username = binding.Username
		}
	}
	if quota, err := GetLightweightVMQuota(vmName); err == nil {
		info.LightweightQuota = quota
	}
	if isLightweightOperator {
		if info.Switch != nil {
			info.Switches = []model.VPCSwitch{*info.Switch}
		} else {
			var user model.User
			if err := model.DB.Where("username = ?", operator).First(&user).Error; err == nil && user.DedicatedVPCSwitchID > 0 {
				var sw model.VPCSwitch
				if err := model.DB.First(&sw, user.DedicatedVPCSwitchID).Error; err == nil {
					normalizeVPCSwitchBandwidthForResponse(&sw)
					info.Switches = []model.VPCSwitch{sw}
				}
			}
		}
		if info.SecurityGroup != nil && info.SecurityGroup.IsVMScoped && info.SecurityGroup.VMName == vmName {
			info.Groups = []model.VPCSecurityGroup{*info.SecurityGroup}
		} else {
			model.DB.Where("vm_name = ? AND is_vm_scoped = ?", vmName, true).Order("id ASC").Find(&info.Groups)
		}
		return info, nil
	}
	if role == "admin" {
		model.DB.Order("username ASC, id ASC").Find(&info.Switches)
		model.DB.Order("username ASC, is_default DESC, id ASC").Find(&info.Groups)
	} else if username != "" {
		model.DB.Where("username = ? AND (bridge_mode = '' OR bridge_mode = ? OR bridge_mode IS NULL)", username, BridgeModeNAT).Order("id ASC").Find(&info.Switches)
		model.DB.Where("username = ?", username).Order("is_default DESC, id ASC").Find(&info.Groups)
	}
	for i := range info.Switches {
		normalizeVPCSwitchBandwidthForResponse(&info.Switches[i])
	}
	return info, nil
}

func SwitchVMSecurityGroup(operator, role, vmName string, securityGroupID uint) error {
	if role != "admin" && IsLightweightCloudUser(operator) {
		return fmt.Errorf("轻量云服务器使用管理员分配的专属安全组，不能切换安全组")
	}
	info, err := GetVPCBindingInfo(operator, role, vmName)
	if err != nil {
		return err
	}
	if info.Binding == nil {
		return fmt.Errorf("请先保存 VPC 绑定，再单独切换安全组")
	}
	var group model.VPCSecurityGroup
	if err := model.DB.Where("id = ? AND username = ?", securityGroupID, info.Binding.Username).First(&group).Error; err != nil {
		return fmt.Errorf("安全组不存在或不属于该用户")
	}
	info.Binding.SecurityGroupID = securityGroupID
	if err := model.DB.Save(info.Binding).Error; err != nil {
		return err
	}
	return ApplyVPCACLRules()
}

func ApplyVPCSwitchRuntime(vmName string, sw model.VPCSwitch) error {
	return applyVPCSwitchRuntime(vmName, sw, true)
}

func applyVPCSwitchRuntime(vmName string, sw model.VPCSwitch, ensureSwitch bool) error {
	if ensureSwitch {
		if err := EnsureVPCSwitchRuntime(sw); err != nil {
			return err
		}
	}
	if SwitchUsesDirectBridge(sw) {
		state := strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout)
		if state == "running" {
			for _, iface := range parseVirshDomiflistOutput(utils.ExecCommand("virsh", "domiflist", vmName).Stdout) {
				if iface.Source == BridgeNameForSwitch(sw) {
					if err := ensureVMBridgeInterfaceConfig(vmName, BridgeNameForSwitch(sw), sw.BridgeVLANID); err != nil {
						return err
					}
					if err := ensureVMDirectBridgeRuntimeVLAN(vmName, sw.BridgeVLANID); err != nil {
						return err
					}
					if ensureSwitch {
						return applyVPCSwitchBandwidth(sw)
					}
					return nil
				}
			}
			return fmt.Errorf("桥接直通交换机切换需要先关闭虚拟机")
		}
		if err := ensureVMBridgeInterfaceConfig(vmName, BridgeNameForSwitch(sw), sw.BridgeVLANID); err != nil {
			return err
		}
		if ensureSwitch {
			return applyVPCSwitchBandwidth(sw)
		}
		return nil
	}
	if strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout) == "running" {
		for _, iface := range parseVirshDomiflistOutput(utils.ExecCommand("virsh", "domiflist", vmName).Stdout) {
			if iface.Type == "bridge" && iface.Source != "" && iface.Source != ovsBridgeName() {
				return fmt.Errorf("从桥接直通交换机切换回 VPC 需要先关闭虚拟机")
			}
		}
	}
	if err := ensureVMVPCInterfaceConfig(vmName, sw.VLANID); err != nil {
		return err
	}
	if mac := getFirstVMMAC(vmName); mac != "" {
		CleanOVSDHCPLease(mac, "")
	}
	if strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout) == "running" {
		if err := ensureVMVPCRuntimeInterfaceConfig(vmName, sw.VLANID); err != nil {
			return err
		}
	}
	vnetIF := getVMVnetIF(vmName)
	if vnetIF == "" {
		return nil
	}
	targetTag := strconv.Itoa(sw.VLANID)
	if currentTag, ok := getOVSPortTag(vnetIF); !ok || currentTag != targetTag {
		result := utils.ExecCommand("ovs-vsctl", "set", "Port", vnetIF, "tag="+targetTag)
		if result.Error != nil {
			return fmt.Errorf("设置 VM OVS VLAN tag 失败: %s", result.Stderr)
		}
	}
	if ensureSwitch {
		if err := applyVPCSwitchBandwidth(sw); err != nil {
			return err
		}
	}
	return nil
}

func getOVSPortTag(port string) (string, bool) {
	if strings.TrimSpace(port) == "" {
		return "", false
	}
	result := utils.ExecCommand("ovs-vsctl", "--if-exists", "get", "Port", port, "tag")
	if result.Error != nil {
		return "", false
	}
	tag := strings.Trim(strings.TrimSpace(result.Stdout), `"`)
	if tag == "" || tag == "[]" {
		return "", false
	}
	return tag, true
}

func validateVMCanApplyVPCSwitch(vmName string, sw model.VPCSwitch) error {
	if strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout) != "running" {
		return nil
	}
	if SwitchUsesDirectBridge(sw) {
		for _, iface := range parseVirshDomiflistOutput(utils.ExecCommand("virsh", "domiflist", vmName).Stdout) {
			if iface.Source == BridgeNameForSwitch(sw) {
				return nil
			}
		}
		return fmt.Errorf("桥接直通交换机切换需要先关闭虚拟机")
	}
	for _, iface := range parseVirshDomiflistOutput(utils.ExecCommand("virsh", "domiflist", vmName).Stdout) {
		if iface.Type == "bridge" && iface.Source != "" && iface.Source != ovsBridgeName() {
			return fmt.Errorf("从桥接直通交换机切换回 VPC 需要先关闭虚拟机")
		}
	}
	return nil
}

func inferVPCSwitchForVM(vmName string) (*model.VPCSwitch, bool) {
	if strings.TrimSpace(vmName) == "" || model.DB == nil {
		return nil, false
	}
	if sw, found := inferDirectBridgeSwitchForVM(vmName); found {
		return repairMissingVPCBindingFromRuntime(vmName, sw)
	}
	if vlanID, ok := inferVPCVLANTagFromVMXML(vmName); ok {
		if sw, found := getVPCSwitchByVLANID(vlanID); found {
			return repairMissingVPCBindingFromRuntime(vmName, sw)
		}
	}
	if vnetIF := getVMVnetIF(vmName); vnetIF != "" {
		if tag, ok := getOVSPortTag(vnetIF); ok {
			if vlanID, err := strconv.Atoi(tag); err == nil && vlanID > 0 {
				if sw, found := getVPCSwitchByVLANID(vlanID); found {
					return repairMissingVPCBindingFromRuntime(vmName, sw)
				}
			}
		}
	}
	if mac := getFirstVMMAC(vmName); mac != "" {
		if sw, found := getVPCSwitchByOVSStaticHost(vmName, mac); found {
			return repairMissingVPCBindingFromRuntime(vmName, sw)
		}
		if sw, found := getVPCSwitchByMACRuntimeState(mac); found {
			return repairMissingVPCBindingFromRuntime(vmName, sw)
		}
	} else if sw, found := getVPCSwitchByOVSStaticHost(vmName, ""); found {
		return repairMissingVPCBindingFromRuntime(vmName, sw)
	}
	return nil, false
}

func inferDirectBridgeSwitchForVM(vmName string) (*model.VPCSwitch, bool) {
	for _, iface := range parseVirshDomiflistOutput(utils.ExecCommand("virsh", "domiflist", vmName).Stdout) {
		if iface.Type == "bridge" && iface.Source != "" && iface.Source != ovsBridgeName() {
			if sw, found := getVPCSwitchByDirectBridge(iface.Source); found {
				return sw, true
			}
		}
	}
	for _, args := range [][]string{{"dumpxml", vmName, "--inactive"}, {"dumpxml", vmName}} {
		result := utils.ExecCommand("virsh", args...)
		if result.Error != nil {
			continue
		}
		if bridge, ok := extractFirstOVSInterfaceBridge(result.Stdout); ok && bridge != ovsBridgeName() {
			if sw, found := getVPCSwitchByDirectBridge(bridge); found {
				return sw, true
			}
		}
	}
	return nil, false
}

func inferVPCVLANTagFromVMXML(vmName string) (int, bool) {
	for _, args := range [][]string{
		{"dumpxml", vmName, "--inactive"},
		{"dumpxml", vmName},
	} {
		result := utils.ExecCommand("virsh", args...)
		if result.Error != nil {
			continue
		}
		if vlanID, ok := extractFirstOVSInterfaceVLANTag(result.Stdout); ok {
			return vlanID, true
		}
	}
	return 0, false
}

func extractFirstOVSInterfaceVLANTag(xmlText string) (int, bool) {
	if strings.TrimSpace(xmlText) == "" {
		return 0, false
	}
	bridge := ovsBridgeName()
	searchFrom := 0
	for {
		startRel := strings.Index(xmlText[searchFrom:], "<interface ")
		if startRel < 0 {
			return 0, false
		}
		start := searchFrom + startRel
		endRel := strings.Index(xmlText[start:], "</interface>")
		if endRel < 0 {
			return 0, false
		}
		end := start + endRel + len("</interface>")
		block := xmlText[start:end]
		if isOVSBridgeInterfaceBlock(block, bridge) {
			match := regexp.MustCompile(`<tag\s+id=['"]([0-9]+)['"]\s*/>`).FindStringSubmatch(block)
			if len(match) == 2 {
				vlanID, err := strconv.Atoi(match[1])
				return vlanID, err == nil && vlanID > 0
			}
			return 0, false
		}
		searchFrom = end
	}
}

func getVPCSwitchByDirectBridge(bridgeName string) (*model.VPCSwitch, bool) {
	if strings.TrimSpace(bridgeName) == "" || model.DB == nil {
		return nil, false
	}
	var sw model.VPCSwitch
	if err := model.DB.Where("bridge_mode = ? AND bridge_name = ?", BridgeModeDirect, strings.TrimSpace(bridgeName)).Order("id ASC").First(&sw).Error; err != nil {
		return nil, false
	}
	return &sw, true
}

func getVPCSwitchByVLANID(vlanID int) (*model.VPCSwitch, bool) {
	if vlanID <= 0 || model.DB == nil {
		return nil, false
	}
	var sw model.VPCSwitch
	if err := model.DB.Where("vlan_id = ?", vlanID).First(&sw).Error; err != nil {
		return nil, false
	}
	return &sw, true
}

func getVPCSwitchByIP(ipText string) (*model.VPCSwitch, bool) {
	ipText = strings.TrimSpace(ipText)
	if ipText == "" || model.DB == nil {
		return nil, false
	}
	var switches []model.VPCSwitch
	if err := model.DB.Order("id ASC").Find(&switches).Error; err != nil {
		return nil, false
	}
	for _, sw := range switches {
		if IPInCIDR(ipText, sw.CIDR) {
			return &sw, true
		}
	}
	return nil, false
}

func getVPCSwitchByOVSStaticHost(vmName, mac string) (*model.VPCSwitch, bool) {
	vmName = strings.TrimSpace(vmName)
	mac = strings.ToLower(strings.TrimSpace(mac))
	hosts := []OVSStaticHost{}
	if vmName != "" {
		if host, ok := GetOVSStaticHostByVMName(vmName); ok {
			hosts = append(hosts, host)
		}
	}
	if mac != "" {
		if ip := GetOVSStaticIPByMAC(mac); ip != "" {
			hosts = append(hosts, OVSStaticHost{VMName: vmName, MAC: mac, IP: ip})
		}
	}
	for _, host := range hosts {
		if sw, ok := getVPCSwitchByIP(host.IP); ok {
			return sw, true
		}
	}
	return nil, false
}

func getVPCSwitchByMACRuntimeState(mac string) (*model.VPCSwitch, bool) {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" || model.DB == nil {
		return nil, false
	}
	var switches []model.VPCSwitch
	if err := model.DB.Order("id ASC").Find(&switches).Error; err != nil {
		return nil, false
	}
	for _, sw := range switches {
		if ip := GetVPCStaticIPByMAC(sw.ID, mac); ip != "" {
			return &sw, true
		}
		leases, err := ListVPCDHCPLeasesForSwitch(sw.ID)
		if err != nil {
			continue
		}
		for _, lease := range leases {
			if strings.EqualFold(lease.MAC, mac) {
				return &sw, true
			}
		}
	}
	return nil, false
}

func repairMissingVPCBindingFromRuntime(vmName string, sw *model.VPCSwitch) (*model.VPCSwitch, bool) {
	if sw == nil || strings.TrimSpace(vmName) == "" || model.DB == nil {
		return sw, sw != nil
	}
	migrateOVSStaticHostToVPCIfNeeded(vmName, *sw)
	group, err := EnsureDefaultSecurityGroup(sw.Username)
	if err != nil {
		fmt.Printf("[警告] VM %s 运行态属于 VPC 交换机 %s，但补默认安全组失败: %v\n", vmName, sw.Name, err)
		return sw, true
	}
	var existing model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&existing).Error; err == nil {
		if existing.SwitchID == sw.ID && existing.SecurityGroupID == group.ID && existing.Username == sw.Username {
			return sw, true
		}
		existing.Username = sw.Username
		existing.SwitchID = sw.ID
		existing.SecurityGroupID = group.ID
		if saveErr := model.DB.Save(&existing).Error; saveErr != nil {
			fmt.Printf("[警告] VM %s VPC 绑定记录修复失败: %v\n", vmName, saveErr)
			return sw, true
		}
		logNetworkRuntimeChange(fmt.Sprintf("已根据运行态修复 VM %s 的 VPC 绑定记录", vmName))
		return sw, true
	}
	binding := model.VPCVMBinding{
		VMName:          vmName,
		Username:        sw.Username,
		SwitchID:        sw.ID,
		SecurityGroupID: group.ID,
	}
	if createErr := model.DB.Create(&binding).Error; createErr != nil {
		fmt.Printf("[警告] VM %s VPC 绑定记录补建失败: %v\n", vmName, createErr)
		return sw, true
	}
	logNetworkRuntimeChange(fmt.Sprintf("已根据运行态补建 VM %s 的 VPC 绑定记录", vmName))
	return sw, true
}

func migrateOVSStaticHostToVPCIfNeeded(vmName string, sw model.VPCSwitch) {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || sw.ID == 0 {
		return
	}
	mac := strings.ToLower(strings.TrimSpace(getFirstVMMAC(vmName)))
	host, ok := GetOVSStaticHostByVMName(vmName)
	if !ok && mac != "" {
		if ip := GetOVSStaticIPByMAC(mac); ip != "" {
			host = OVSStaticHost{VMName: vmName, MAC: mac, IP: ip}
			ok = true
		}
	}
	if !ok || strings.TrimSpace(host.IP) == "" || !IPInCIDR(host.IP, sw.CIDR) {
		return
	}
	host.VMName = vmName
	host.MAC = strings.ToLower(strings.TrimSpace(host.MAC))
	if host.MAC == "" {
		host.MAC = mac
	}
	if host.MAC == "" {
		return
	}
	if err := UpsertVPCStaticHost(sw, host.VMName, host.MAC, host.IP); err != nil {
		fmt.Printf("[警告] 迁移 VM %s 的 VPC 静态 IP 绑定失败: %v\n", vmName, err)
		return
	}
	_, _ = RemoveOVSStaticHost(host.VMName, host.MAC)
	logNetworkRuntimeChange(fmt.Sprintf("已将 VM %s 的静态 IP %s 迁移到 VPC 交换机 %s", vmName, host.IP, sw.Name))
}

func ApplyAllVPCBindingsRuntime() error {
	return applyAllVPCBindingsRuntime(true)
}

func applyAllVPCBindingsRuntime(ensureSwitch bool) error {
	if model.DB == nil {
		return nil
	}
	var bindings []model.VPCVMBinding
	if err := model.DB.Order("vm_name ASC").Find(&bindings).Error; err != nil {
		return err
	}
	var lastErr error
	switches := map[uint]model.VPCSwitch{}
	touchedSwitches := map[uint]model.VPCSwitch{}
	ensuredSwitches := map[uint]bool{}
	for _, binding := range bindings {
		if vmLibvirtDomainMissing(binding.VMName) {
			fmt.Printf("[提示] VM %s 已不存在，清理残留 VPC 绑定并跳过运行态恢复\n", binding.VMName)
			CleanupVMVPCBinding(binding.VMName)
			continue
		}
		sw, ok := switches[binding.SwitchID]
		if !ok {
			if err := model.DB.First(&sw, binding.SwitchID).Error; err != nil {
				lastErr = err
				fmt.Printf("[警告] VM %s 绑定的 VPC 交换机 %d 不存在: %v\n", binding.VMName, binding.SwitchID, err)
				continue
			}
			switches[binding.SwitchID] = sw
		}
		if ensureSwitch && !ensuredSwitches[sw.ID] {
			if err := EnsureVPCSwitchRuntime(sw); err != nil {
				lastErr = err
				fmt.Printf("[警告] 恢复 VPC 交换机 %s(%d) 运行态失败: %v\n", sw.Name, sw.ID, err)
				continue
			}
			ensuredSwitches[sw.ID] = true
		}
		if err := applyVPCSwitchRuntime(binding.VMName, sw, false); err != nil {
			lastErr = err
			fmt.Printf("[警告] 恢复 VM %s 的 VPC 运行态失败: %v\n", binding.VMName, err)
			continue
		}
		touchedSwitches[sw.ID] = sw
	}
	for _, sw := range touchedSwitches {
		if err := applyVPCSwitchBandwidth(sw); err != nil {
			lastErr = err
			fmt.Printf("[警告] 恢复 VPC 交换机 %s(%d) 带宽策略失败: %v\n", sw.Name, sw.ID, err)
		}
	}
	return lastErr
}

func vmLibvirtDomainMissing(vmName string) bool {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return false
	}
	result := utils.ExecCommand("virsh", "domstate", vmName)
	return isLibvirtDomainMissingResult(result)
}

func isLibvirtDomainMissingResult(result *utils.CmdResult) bool {
	if result == nil || result.Error == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		result.Stdout,
		result.Stderr,
		result.Error.Error(),
	}, "\n"))
	return strings.Contains(text, "failed to get domain") ||
		strings.Contains(text, "domain not found") ||
		strings.Contains(text, "no domain with matching name") ||
		strings.Contains(text, "domain is not found")
}

func ensureVMVPCInterfaceConfig(vmName string, vlanID int) error {
	if strings.TrimSpace(vmName) == "" || vlanID <= 0 {
		return nil
	}
	result := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive")
	if result.Error != nil {
		result = utils.ExecCommand("virsh", "dumpxml", vmName)
	}
	if result.Error != nil {
		return fmt.Errorf("读取 VM XML 失败: %s", result.Stderr)
	}
	if firstOVSInterfaceHasVLANTag(result.Stdout, vlanID) && firstOVSInterfaceUsesBridge(result.Stdout, ovsBridgeName()) {
		return nil
	}
	updated, changed := setFirstOVSInterfaceVPC(result.Stdout, vlanID)
	if !changed {
		return nil
	}
	xmlPath := filepath.Join(os.TempDir(), fmt.Sprintf("_vpc-vlan-%s.xml", safeVMXMLFileName(vmName)))
	if err := os.WriteFile(xmlPath, []byte(updated), 0600); err != nil {
		return fmt.Errorf("写入 VM VPC XML 失败: %w", err)
	}
	defer os.Remove(xmlPath)
	define := utils.ExecCommand("virsh", "define", xmlPath)
	if define.Error != nil {
		return fmt.Errorf("持久化 VM VPC VLAN 失败: %s", define.Stderr)
	}
	return nil
}

func ensureVMVPCRuntimeInterfaceConfig(vmName string, vlanID int) error {
	if strings.TrimSpace(vmName) == "" || vlanID <= 0 {
		return nil
	}
	result := utils.ExecCommand("virsh", "dumpxml", vmName)
	if result.Error != nil {
		return fmt.Errorf("读取运行中 VM XML 失败: %s", result.Stderr)
	}
	if firstOVSInterfaceHasVLANTag(result.Stdout, vlanID) && firstOVSInterfaceUsesBridge(result.Stdout, ovsBridgeName()) {
		return nil
	}
	updated, changed := setFirstOVSInterfaceVPC(result.Stdout, vlanID)
	if !changed {
		return nil
	}
	currentBlock, ok := extractFirstOVSInterfaceBlock(result.Stdout)
	if !ok {
		return nil
	}
	updatedBlock, ok := extractFirstOVSInterfaceBlock(updated)
	if !ok {
		return nil
	}
	if mac := getFirstVMMAC(vmName); mac != "" {
		CleanAllVPCDHCPLeases(mac, "")
		CleanOVSDHCPLease(mac, "")
	}
	return replugVMInterfaceLive(vmName, currentBlock, updatedBlock)
}

func replugVMInterfaceLive(vmName, currentBlock, updatedBlock string) error {
	currentBlock = stripRuntimeOnlyInterfaceElements(currentBlock)
	updatedBlock = stripRuntimeOnlyInterfaceElements(updatedBlock)
	if strings.TrimSpace(currentBlock) == "" || strings.TrimSpace(updatedBlock) == "" {
		return nil
	}
	detachPath := filepath.Join(os.TempDir(), fmt.Sprintf("_vm-net-detach-%s.xml", safeVMXMLFileName(vmName)))
	attachPath := filepath.Join(os.TempDir(), fmt.Sprintf("_vm-net-attach-%s.xml", safeVMXMLFileName(vmName)))
	if err := os.WriteFile(detachPath, []byte(currentBlock), 0600); err != nil {
		return fmt.Errorf("写入运行态网卡 detach XML 失败: %w", err)
	}
	defer os.Remove(detachPath)
	if err := os.WriteFile(attachPath, []byte(updatedBlock), 0600); err != nil {
		return fmt.Errorf("写入运行态网卡 attach XML 失败: %w", err)
	}
	defer os.Remove(attachPath)
	detach := utils.ExecCommandWithTimeout("virsh", 60*time.Second, "detach-device", vmName, detachPath, "--live")
	if detach.Error != nil {
		return fmt.Errorf("热拔 VM 网卡失败: %s", firstNonEmpty(detach.Stderr, detach.Error.Error()))
	}
	time.Sleep(2 * time.Second)
	attach := utils.ExecCommandWithTimeout("virsh", 60*time.Second, "attach-device", vmName, attachPath, "--live")
	if attach.Error != nil {
		restore := utils.ExecCommandWithTimeout("virsh", 60*time.Second, "attach-device", vmName, detachPath, "--live")
		if restore.Error != nil {
			return fmt.Errorf("热插 VM 网卡失败: %s；恢复原网卡也失败: %s", firstNonEmpty(attach.Stderr, attach.Error.Error()), firstNonEmpty(restore.Stderr, restore.Error.Error()))
		}
		return fmt.Errorf("热插 VM 网卡失败，已恢复原网卡: %s", firstNonEmpty(attach.Stderr, attach.Error.Error()))
	}
	return nil
}

func ensureVMBridgeInterfaceConfig(vmName, bridge string, vlanID int) error {
	if strings.TrimSpace(vmName) == "" || strings.TrimSpace(bridge) == "" {
		return nil
	}
	result := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive")
	if result.Error != nil {
		result = utils.ExecCommand("virsh", "dumpxml", vmName)
	}
	if result.Error != nil {
		return fmt.Errorf("读取 VM XML 失败: %s", result.Stderr)
	}
	updated, changed := setFirstOVSInterfaceDirectBridge(result.Stdout, bridge, vlanID)
	if !changed {
		return nil
	}
	xmlPath := filepath.Join(os.TempDir(), fmt.Sprintf("_vm-bridge-%s.xml", safeVMXMLFileName(vmName)))
	if err := os.WriteFile(xmlPath, []byte(updated), 0600); err != nil {
		return fmt.Errorf("写入 VM 桥接 XML 失败: %w", err)
	}
	defer os.Remove(xmlPath)
	define := utils.ExecCommand("virsh", "define", xmlPath)
	if define.Error != nil {
		return fmt.Errorf("持久化 VM 桥接配置失败: %s", define.Stderr)
	}
	return nil
}

func ensureVMDirectBridgeRuntimeVLAN(vmName string, vlanID int) error {
	vnetIF := getVMVnetIF(vmName)
	if vnetIF == "" {
		return nil
	}
	if vlanID > 0 {
		targetTag := strconv.Itoa(vlanID)
		if currentTag, ok := getOVSPortTag(vnetIF); ok && currentTag == targetTag {
			return nil
		}
		result := utils.ExecCommand("ovs-vsctl", "set", "Port", vnetIF, "tag="+targetTag)
		if result.Error != nil {
			return fmt.Errorf("设置桥接 VM OVS VLAN tag 失败: %s", result.Stderr)
		}
		return nil
	}
	result := utils.ExecCommand("ovs-vsctl", "remove", "Port", vnetIF, "tag", "0")
	if result.Error != nil {
		result = utils.ExecCommand("ovs-vsctl", "clear", "Port", vnetIF, "tag")
	}
	if result.Error != nil {
		return fmt.Errorf("清理桥接 VM OVS VLAN tag 失败: %s", result.Stderr)
	}
	return nil
}

func firstOVSInterfaceHasVLANTag(xmlText string, vlanID int) bool {
	if strings.TrimSpace(xmlText) == "" || vlanID <= 0 {
		return false
	}
	current, ok := extractFirstOVSInterfaceVLANTag(xmlText)
	return ok && current == vlanID
}

func firstOVSInterfaceUsesBridge(xmlText, bridge string) bool {
	if strings.TrimSpace(xmlText) == "" || strings.TrimSpace(bridge) == "" {
		return false
	}
	current, ok := extractFirstOVSInterfaceBridge(xmlText)
	return ok && current == strings.TrimSpace(bridge)
}

func extractFirstOVSInterfaceBridge(xmlText string) (string, bool) {
	if strings.TrimSpace(xmlText) == "" {
		return "", false
	}
	searchFrom := 0
	for {
		startRel := strings.Index(xmlText[searchFrom:], "<interface ")
		if startRel < 0 {
			return "", false
		}
		start := searchFrom + startRel
		endRel := strings.Index(xmlText[start:], "</interface>")
		if endRel < 0 {
			return "", false
		}
		end := start + endRel + len("</interface>")
		block := xmlText[start:end]
		hasBridgeType := strings.Contains(block, "<interface type='bridge'") || strings.Contains(block, `<interface type="bridge"`)
		hasOVS := strings.Contains(block, "virtualport type='openvswitch'") || strings.Contains(block, `virtualport type="openvswitch"`)
		if hasBridgeType && hasOVS {
			sourceRe := regexp.MustCompile(`<source\s+bridge=['"]([^'"]+)['"]\s*/>`)
			match := sourceRe.FindStringSubmatch(block)
			if len(match) == 2 {
				return match[1], true
			}
			return "", false
		}
		searchFrom = end
	}
}

func extractFirstOVSInterfaceBlock(xmlText string) (string, bool) {
	if strings.TrimSpace(xmlText) == "" {
		return "", false
	}
	searchFrom := 0
	for {
		startRel := strings.Index(xmlText[searchFrom:], "<interface ")
		if startRel < 0 {
			return "", false
		}
		start := searchFrom + startRel
		endRel := strings.Index(xmlText[start:], "</interface>")
		if endRel < 0 {
			return "", false
		}
		end := start + endRel + len("</interface>")
		block := xmlText[start:end]
		hasBridgeType := strings.Contains(block, "<interface type='bridge'") || strings.Contains(block, `<interface type="bridge"`)
		hasOVS := strings.Contains(block, "virtualport type='openvswitch'") || strings.Contains(block, `virtualport type="openvswitch"`)
		if hasBridgeType && hasOVS {
			return block, true
		}
		searchFrom = end
	}
}

func stripRuntimeOnlyInterfaceElements(block string) string {
	if strings.TrimSpace(block) == "" {
		return block
	}
	runtimeRe := regexp.MustCompile(`(?m)\n\s*<(target|alias|address)\b[^>]*/>`)
	return runtimeRe.ReplaceAllString(block, "")
}

func safeVMXMLFileName(vmName string) string {
	value := regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(strings.TrimSpace(vmName), "_")
	if value == "" {
		return "vm"
	}
	return value
}

func setFirstOVSInterfaceVPC(xmlText string, vlanID int) (string, bool) {
	if strings.TrimSpace(xmlText) == "" || vlanID <= 0 {
		return xmlText, false
	}
	updated := xmlText
	bridgeUpdated, bridgeChanged := setFirstOVSInterfaceBridge(updated, ovsBridgeName())
	if bridgeChanged {
		updated = bridgeUpdated
	}
	vlanUpdated, vlanChanged := setFirstOVSInterfaceVLANTag(updated, vlanID)
	if vlanChanged {
		updated = vlanUpdated
	}
	return updated, bridgeChanged || vlanChanged
}

func setFirstOVSInterfaceDirectBridge(xmlText, bridge string, vlanID int) (string, bool) {
	updated, bridgeChanged := setFirstOVSInterfaceBridge(xmlText, bridge)
	if !bridgeChanged && !firstOVSInterfaceUsesBridge(updated, bridge) {
		return xmlText, false
	}
	if vlanID > 0 {
		vlanUpdated, vlanChanged := setFirstOVSInterfaceAnyVLANTag(updated, vlanID)
		return vlanUpdated, bridgeChanged || vlanChanged
	}
	vlanUpdated := removeFirstInterfaceVLAN(updated)
	return vlanUpdated, bridgeChanged || vlanUpdated != updated
}

func setFirstOVSInterfaceVLANTag(xmlText string, vlanID int) (string, bool) {
	if strings.TrimSpace(xmlText) == "" || vlanID <= 0 {
		return xmlText, false
	}
	bridge := ovsBridgeName()
	searchFrom := 0
	for {
		startRel := strings.Index(xmlText[searchFrom:], "<interface ")
		if startRel < 0 {
			return xmlText, false
		}
		start := searchFrom + startRel
		endRel := strings.Index(xmlText[start:], "</interface>")
		if endRel < 0 {
			return xmlText, false
		}
		end := start + endRel + len("</interface>")
		block := xmlText[start:end]
		if isOVSBridgeInterfaceBlock(block, bridge) {
			updatedBlock, changed := setInterfaceBlockVLANTag(block, vlanID)
			if !changed {
				return xmlText, false
			}
			return xmlText[:start] + updatedBlock + xmlText[end:], true
		}
		searchFrom = end
	}
}

func setFirstOVSInterfaceAnyVLANTag(xmlText string, vlanID int) (string, bool) {
	if strings.TrimSpace(xmlText) == "" || vlanID <= 0 {
		return xmlText, false
	}
	searchFrom := 0
	for {
		startRel := strings.Index(xmlText[searchFrom:], "<interface ")
		if startRel < 0 {
			return xmlText, false
		}
		start := searchFrom + startRel
		endRel := strings.Index(xmlText[start:], "</interface>")
		if endRel < 0 {
			return xmlText, false
		}
		end := start + endRel + len("</interface>")
		block := xmlText[start:end]
		hasBridgeType := strings.Contains(block, "<interface type='bridge'") || strings.Contains(block, `<interface type="bridge"`)
		hasOVS := strings.Contains(block, "virtualport type='openvswitch'") || strings.Contains(block, `virtualport type="openvswitch"`)
		if hasBridgeType && hasOVS {
			updatedBlock, changed := setInterfaceBlockVLANTag(block, vlanID)
			if !changed {
				return xmlText, false
			}
			return xmlText[:start] + updatedBlock + xmlText[end:], true
		}
		searchFrom = end
	}
}

func setFirstOVSInterfaceBridge(xmlText, bridge string) (string, bool) {
	if strings.TrimSpace(xmlText) == "" || strings.TrimSpace(bridge) == "" {
		return xmlText, false
	}
	searchFrom := 0
	for {
		startRel := strings.Index(xmlText[searchFrom:], "<interface ")
		if startRel < 0 {
			return xmlText, false
		}
		start := searchFrom + startRel
		endRel := strings.Index(xmlText[start:], "</interface>")
		if endRel < 0 {
			return xmlText, false
		}
		end := start + endRel + len("</interface>")
		block := xmlText[start:end]
		hasBridgeType := strings.Contains(block, "<interface type='bridge'") || strings.Contains(block, `<interface type="bridge"`)
		hasOVS := strings.Contains(block, "virtualport type='openvswitch'") || strings.Contains(block, `virtualport type="openvswitch"`)
		if hasBridgeType && hasOVS {
			sourceRe := regexp.MustCompile(`<source\s+bridge=['"][^'"]+['"]\s*/>`)
			updatedBlock := sourceRe.ReplaceAllString(block, fmt.Sprintf("<source bridge='%s'/>", strings.TrimSpace(bridge)))
			return xmlText[:start] + updatedBlock + xmlText[end:], updatedBlock != block
		}
		searchFrom = end
	}
}

func removeFirstInterfaceVLAN(xmlText string) string {
	searchFrom := 0
	for {
		startRel := strings.Index(xmlText[searchFrom:], "<interface ")
		if startRel < 0 {
			return xmlText
		}
		start := searchFrom + startRel
		endRel := strings.Index(xmlText[start:], "</interface>")
		if endRel < 0 {
			return xmlText
		}
		end := start + endRel + len("</interface>")
		block := xmlText[start:end]
		hasOVS := strings.Contains(block, "virtualport type='openvswitch'") || strings.Contains(block, `virtualport type="openvswitch"`)
		if hasOVS {
			vlanRe := regexp.MustCompile(`(?s)\n\s*<vlan>.*?</vlan>`)
			updatedBlock := vlanRe.ReplaceAllString(block, "")
			return xmlText[:start] + updatedBlock + xmlText[end:]
		}
		searchFrom = end
	}
}

func isOVSBridgeInterfaceBlock(block, bridge string) bool {
	hasBridgeType := strings.Contains(block, "<interface type='bridge'") || strings.Contains(block, `<interface type="bridge"`)
	hasOVS := strings.Contains(block, "virtualport type='openvswitch'") || strings.Contains(block, `virtualport type="openvswitch"`)
	hasSource := strings.Contains(block, fmt.Sprintf("source bridge='%s'", bridge)) || strings.Contains(block, fmt.Sprintf(`source bridge="%s"`, bridge))
	return hasBridgeType && hasOVS && hasSource
}

func setInterfaceBlockVLANTag(block string, vlanID int) (string, bool) {
	if regexp.MustCompile(fmt.Sprintf(`<tag\s+id=['"]%d['"]\s*/>`, vlanID)).MatchString(block) {
		return block, true
	}
	indent := "      "
	if match := regexp.MustCompile(`(?m)^(\s*)<interface\s`).FindStringSubmatch(block); len(match) > 1 {
		indent = match[1] + "  "
	}
	vlanBlock := fmt.Sprintf("%s<vlan>\n%s  <tag id='%d'/>\n%s</vlan>", indent, indent, vlanID, indent)
	vlanRe := regexp.MustCompile(`(?s)\n\s*<vlan>.*?</vlan>`)
	if vlanRe.MatchString(block) {
		return vlanRe.ReplaceAllString(block, "\n"+vlanBlock), true
	}
	closeIdx := strings.LastIndex(block, "</interface>")
	if closeIdx < 0 {
		return block, false
	}
	return block[:closeIdx] + vlanBlock + "\n" + block[closeIdx:], true
}

func ApplyVPCBindingRuntime(vmName string) error {
	if model.DB == nil || strings.TrimSpace(vmName) == "" {
		return nil
	}
	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&binding).Error; err != nil {
		return nil
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, binding.SwitchID).Error; err != nil {
		return fmt.Errorf("VPC 交换机不存在: %w", err)
	}
	return ApplyVPCSwitchRuntime(vmName, sw)
}

func EnsureVPCForVMCreate(username string, switchID, securityGroupID uint) error {
	_, _, err := ResolveVPCForVMCreate(username, switchID, securityGroupID)
	return err
}

func ResolveVPCForVMCreate(username string, switchID, securityGroupID uint) (uint, uint, error) {
	if _, err := EnsureDefaultSecurityGroup(username); err != nil {
		return 0, 0, err
	}
	if _, err := EnsureDefaultVPCSwitch(username); err != nil {
		return 0, 0, err
	}
	if switchID == 0 {
		resolvedSwitchID, err := resolveDefaultVPCSwitchIDForVMCreate(username)
		if err != nil {
			return 0, 0, err
		}
		switchID = resolvedSwitchID
	}
	if securityGroupID == 0 {
		resolvedGroupID, err := resolveDefaultVPCSecurityGroupIDForVMCreate(username)
		if err != nil {
			return 0, 0, err
		}
		securityGroupID = resolvedGroupID
	}
	var switchCount int64
	model.DB.Model(&model.VPCSwitch{}).Where("username = ?", username).Count(&switchCount)
	if switchCount == 0 {
		return 0, 0, fmt.Errorf("请先在 VPC 网络中创建交换机后再创建虚拟机")
	}
	var sw model.VPCSwitch
	if err := model.DB.Where("id = ? AND username = ?", switchID, username).First(&sw).Error; err != nil {
		return 0, 0, fmt.Errorf("交换机不存在或不属于当前用户")
	}
	if SwitchUsesDirectBridge(sw) {
		return 0, 0, fmt.Errorf("桥接直通交换机仅管理员可用于创建虚拟机")
	}
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err == nil {
		if user.MaxTrafficDown > 0 && sw.TrafficDownGB <= 0 {
			return 0, 0, fmt.Errorf("交换机下行月流量配额不足，无法创建虚拟机")
		}
		if user.MaxTrafficUp > 0 && sw.TrafficUpGB <= 0 {
			return 0, 0, fmt.Errorf("交换机上行月流量配额不足，无法创建虚拟机")
		}
		if user.MaxBandwidthDown > 0 && sw.BandwidthDownMbps <= 0 {
			return 0, 0, fmt.Errorf("交换机下行总带宽配额不足，无法创建虚拟机")
		}
		if user.MaxBandwidthUp > 0 && sw.BandwidthUpMbps <= 0 {
			return 0, 0, fmt.Errorf("交换机上行总带宽配额不足，无法创建虚拟机")
		}
	}
	var sg model.VPCSecurityGroup
	if err := model.DB.Where("id = ? AND username = ?", securityGroupID, username).First(&sg).Error; err != nil {
		return 0, 0, fmt.Errorf("安全组不存在或不属于当前用户")
	}
	return switchID, securityGroupID, nil
}

func resolveDefaultVPCSwitchIDForVMCreate(username string) (uint, error) {
	var switches []model.VPCSwitch
	if err := model.DB.Where("username = ?", username).Order("id ASC").Find(&switches).Error; err != nil {
		return 0, err
	}
	if len(switches) == 0 {
		return 0, fmt.Errorf("请先在 VPC 网络中创建交换机后再创建虚拟机")
	}
	if len(switches) == 1 {
		return switches[0].ID, nil
	}
	for _, sw := range switches {
		if sw.Name == defaultVPCSwitchName {
			return sw.ID, nil
		}
	}
	return 0, fmt.Errorf("请选择要接入的 VPC 交换机")
}

func resolveDefaultVPCSecurityGroupIDForVMCreate(username string) (uint, error) {
	var group model.VPCSecurityGroup
	if err := model.DB.Where("username = ? AND is_default = ?", username, true).First(&group).Error; err == nil {
		return group.ID, nil
	}
	if err := model.DB.Where("username = ?", username).Order("id ASC").First(&group).Error; err == nil {
		return group.ID, nil
	}
	return 0, fmt.Errorf("请选择要应用的安全组")
}

func EnsureSecurityGroupAllowsPortForward(vmName, protocol, portText string) error {
	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&binding).Error; err != nil {
		return nil
	}
	portStart, portEnd, err := parsePortForwardPortRange(portText)
	if err != nil {
		return err
	}
	protocols := []string{strings.ToLower(strings.TrimSpace(protocol))}
	if protocols[0] == "" {
		protocols[0] = "tcp"
	}
	if protocols[0] == "both" {
		protocols = []string{"tcp", "udp"}
	}
	for _, proto := range protocols {
		var count int64
		model.DB.Model(&model.VPCSecurityGroupRule{}).
			Where("security_group_id = ? AND direction = ? AND protocol = ? AND port_start <= ? AND port_end >= ? AND target_type = ? AND target_value = ?",
				binding.SecurityGroupID, "ingress", proto, portStart, portEnd, "cidr", "0.0.0.0/0").
			Count(&count)
		if count > 0 {
			continue
		}
		rule := model.VPCSecurityGroupRule{
			SecurityGroupID: binding.SecurityGroupID,
			Direction:       "ingress",
			Protocol:        proto,
			PortStart:       portStart,
			PortEnd:         portEnd,
			TargetType:      "cidr",
			TargetValue:     "0.0.0.0/0",
			Remark:          autoPortForwardSecurityGroupRuleNote,
		}
		if err := model.DB.Create(&rule).Error; err != nil {
			return err
		}
	}
	return ApplyVPCACLRules()
}

func parsePortForwardPortRange(portText string) (int, int, error) {
	ports := strings.Split(strings.ReplaceAll(strings.TrimSpace(portText), "-", ":"), ":")
	if len(ports) == 0 || strings.TrimSpace(ports[0]) == "" {
		return 0, 0, fmt.Errorf("端口不能为空")
	}
	portStart, err := strconv.Atoi(strings.TrimSpace(ports[0]))
	if err != nil || portStart <= 0 || portStart > 65535 {
		return 0, 0, fmt.Errorf("端口格式无效")
	}
	portEnd := portStart
	if len(ports) > 1 {
		portEnd, err = strconv.Atoi(strings.TrimSpace(ports[1]))
		if err != nil || portEnd <= 0 || portEnd > 65535 {
			return 0, 0, fmt.Errorf("端口格式无效")
		}
	}
	if portEnd < portStart {
		return 0, 0, fmt.Errorf("端口范围无效")
	}
	return portStart, portEnd, nil
}

func RemoveSecurityGroupAllowsPortForwardIfUnused(destIP, protocol, portText string) error {
	destIP = strings.TrimSpace(destIP)
	if destIP == "" || strings.TrimSpace(portText) == "" || !IsVPCManagedIP(destIP) {
		return nil
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "tcp"
	}
	portStart, portEnd, err := parsePortForwardPortRange(portText)
	if err != nil {
		return err
	}
	binding, ok, err := findVPCBindingByIP(destIP)
	if err != nil || !ok {
		return err
	}
	if portForwardTargetStillExists(destIP, protocol, portStart, portEnd) {
		return nil
	}
	deleted, err := deleteAutoSecurityGroupPortForwardRules(binding.SecurityGroupID, protocol, portStart, portEnd)
	if err != nil || deleted == 0 {
		return err
	}
	return ApplyVPCACLRules()
}

func portForwardTargetStillExists(destIP, protocol string, portStart, portEnd int) bool {
	rules, err := listLivePortForwardsFromIPTables()
	if err != nil {
		return false
	}
	for _, rule := range rules {
		if strings.TrimSpace(rule.DestIP) != strings.TrimSpace(destIP) {
			continue
		}
		if strings.ToLower(strings.TrimSpace(rule.Protocol)) != strings.ToLower(strings.TrimSpace(protocol)) {
			continue
		}
		ruleStart, ruleEnd, err := parsePortForwardPortRange(rule.DestPort)
		if err != nil {
			continue
		}
		if ruleStart == portStart && ruleEnd == portEnd {
			return true
		}
	}
	return false
}

func deleteAutoSecurityGroupPortForwardRules(securityGroupID uint, protocol string, portStart, portEnd int) (int64, error) {
	result := model.DB.Where(
		"security_group_id = ? AND direction = ? AND protocol = ? AND port_start = ? AND port_end = ? AND target_type = ? AND target_value = ? AND remark = ?",
		securityGroupID, "ingress", strings.ToLower(strings.TrimSpace(protocol)), portStart, portEnd, "cidr", "0.0.0.0/0", autoPortForwardSecurityGroupRuleNote,
	).Delete(&model.VPCSecurityGroupRule{})
	return result.RowsAffected, result.Error
}

func findVPCBindingByIP(ipText string) (*model.VPCVMBinding, bool, error) {
	ipText = strings.TrimSpace(ipText)
	if ipText == "" || model.DB == nil {
		return nil, false, nil
	}
	var switches []model.VPCSwitch
	if err := model.DB.Find(&switches).Error; err != nil {
		return nil, false, err
	}
	for _, sw := range switches {
		if !IPInCIDR(ipText, sw.CIDR) {
			continue
		}
		if binding, ok, err := findVPCBindingByStaticHostIP(sw.ID, ipText); err != nil || ok {
			return binding, ok, err
		}
		if binding, ok, err := findVPCBindingByLeaseIP(sw.ID, ipText); err != nil || ok {
			return binding, ok, err
		}
		var bindings []model.VPCVMBinding
		if err := model.DB.Where("switch_id = ?", sw.ID).Find(&bindings).Error; err != nil {
			return nil, false, err
		}
		for _, binding := range bindings {
			if getFirewallVMIP(binding.VMName) == ipText {
				return &binding, true, nil
			}
		}
	}
	return nil, false, nil
}

func findVPCBindingByStaticHostIP(switchID uint, ipText string) (*model.VPCVMBinding, bool, error) {
	hosts, err := ListVPCStaticHosts(switchID)
	if err != nil {
		return nil, false, err
	}
	for _, host := range hosts {
		if strings.TrimSpace(host.IP) != ipText {
			continue
		}
		var binding model.VPCVMBinding
		query := model.DB.Where("switch_id = ?", switchID)
		if strings.TrimSpace(host.VMName) != "" {
			if err := query.Where("vm_name = ?", strings.TrimSpace(host.VMName)).First(&binding).Error; err == nil {
				return &binding, true, nil
			}
		}
		if strings.TrimSpace(host.MAC) == "" {
			continue
		}
		var bindings []model.VPCVMBinding
		if err := model.DB.Where("switch_id = ?", switchID).Find(&bindings).Error; err != nil {
			return nil, false, err
		}
		for _, candidate := range bindings {
			if strings.EqualFold(getFirstVMMAC(candidate.VMName), host.MAC) {
				return &candidate, true, nil
			}
		}
	}
	return nil, false, nil
}

func findVPCBindingByLeaseIP(switchID uint, ipText string) (*model.VPCVMBinding, bool, error) {
	leases, err := ListVPCDHCPLeasesForSwitch(switchID)
	if err != nil {
		return nil, false, err
	}
	for _, lease := range leases {
		if strings.TrimSpace(lease.IP) != ipText {
			continue
		}
		var bindings []model.VPCVMBinding
		if err := model.DB.Where("switch_id = ?", switchID).Find(&bindings).Error; err != nil {
			return nil, false, err
		}
		for _, binding := range bindings {
			if strings.EqualFold(getFirstVMMAC(binding.VMName), lease.MAC) {
				return &binding, true, nil
			}
		}
	}
	return nil, false, nil
}

func BuildVPCACLRules() (string, error) {
	table := config.GlobalConfig.VPCACLTable
	if strings.TrimSpace(table) == "" {
		table = "kvm_console_vpc_acl"
	}
	var bindings []model.VPCVMBinding
	model.DB.Find(&bindings)
	var b strings.Builder
	b.WriteString("table inet ")
	b.WriteString(table)
	b.WriteString(" {\n")
	b.WriteString("  chain forward {\n")
	b.WriteString("    type filter hook forward priority -40; policy accept;\n")
	var vmIPs []string
	for _, binding := range bindings {
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, binding.SwitchID).Error; err == nil && SwitchUsesDirectBridge(sw) {
			continue
		}
		bindingIPs := vpcFirewallIPsForVM(binding.VMName)
		if len(bindingIPs) == 0 {
			continue
		}
		for _, vmIP := range bindingIPs {
			allows, err := buildVPCIngressAllowRules(binding, vmIP)
			if err != nil {
				return "", err
			}
			for _, line := range allows {
				b.WriteString(line)
			}
			b.WriteString(fmt.Sprintf("    ct status dnat ip daddr %s reject\n", vmIP))
			vmIPs = append(vmIPs, vmIP)
		}
	}
	b.WriteString("    ct state established,related accept\n")
	sort.Strings(vmIPs)
	for _, vmIP := range vmIPs {
		b.WriteString(fmt.Sprintf("    ip daddr %s reject\n", vmIP))
	}
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String(), nil
}

func vpcFirewallIPsForVM(vmName string) []string {
	candidates := []string{getFirewallVMIP(vmName)}
	candidates = append(candidates, PublicIPNATPrivateIPsForVM(vmName)...)
	seen := map[string]bool{}
	var ips []string
	for _, candidate := range candidates {
		ip := normalizeFirewallIPv4(candidate)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	return ips
}

func normalizeFirewallIPv4(ipText string) string {
	ipText = strings.TrimSpace(ipText)
	if ipText == "" || ipText == "unknown" {
		return ""
	}
	ipText = strings.Fields(ipText)[0]
	ipText = strings.TrimSuffix(ipText, "(静态)")
	if addr, err := netip.ParseAddr(ipText); err == nil && addr.Is4() {
		return ipText
	}
	return ""
}

func buildVPCIngressAllowRules(binding model.VPCVMBinding, vmIP string) ([]string, error) {
	var rules []model.VPCSecurityGroupRule
	model.DB.Where("security_group_id = ? AND direction = ?", binding.SecurityGroupID, "ingress").Find(&rules)
	var lines []string
	for _, rule := range rules {
		sources, err := resolveRuleSources(rule)
		if err != nil {
			return nil, err
		}
		for _, src := range sources {
			match := fmt.Sprintf("    ip daddr %s ip saddr %s", vmIP, src)
			switch rule.Protocol {
			case "tcp", "udp":
				portMatch := strconv.Itoa(rule.PortStart)
				if rule.PortEnd > rule.PortStart {
					portMatch = fmt.Sprintf("%d-%d", rule.PortStart, rule.PortEnd)
				}
				match += fmt.Sprintf(" %s dport %s accept\n", rule.Protocol, portMatch)
			case "icmp":
				match += " icmp type echo-request accept\n"
			default:
				match += " accept\n"
			}
			lines = append(lines, match)
		}
	}
	sort.Strings(lines)
	return lines, nil
}

func resolveRuleSources(rule model.VPCSecurityGroupRule) ([]string, error) {
	switch rule.TargetType {
	case "cidr":
		return []string{normalizeCIDROrIP(rule.TargetValue)}, nil
	case "switch":
		id, _ := strconv.Atoi(rule.TargetValue)
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, id).Error; err != nil {
			return nil, fmt.Errorf("安全组规则引用的交换机不存在")
		}
		return []string{sw.CIDR}, nil
	case "security_group":
		id, _ := strconv.Atoi(rule.TargetValue)
		var bindings []model.VPCVMBinding
		model.DB.Where("security_group_id = ?", id).Find(&bindings)
		var sources []string
		for _, binding := range bindings {
			for _, ip := range vpcFirewallIPsForVM(binding.VMName) {
				sources = append(sources, ip+"/32")
			}
		}
		if len(sources) == 0 {
			return []string{"255.255.255.255/32"}, nil
		}
		return sources, nil
	default:
		return nil, fmt.Errorf("安全组规则目标类型无效")
	}
}

func PreviewVPCACLRules() (string, error) {
	return BuildVPCACLRules()
}

func ApplyVPCACLRules() error {
	rules, err := BuildVPCACLRules()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(vpcConfigDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(vpcConfigDir, "acl.nft")
	if err := os.WriteFile(path, []byte(rules), 0644); err != nil {
		return fmt.Errorf("写入 VPC ACL 规则失败: %w", err)
	}
	check := utils.ExecCommand("nft", "-c", "-f", path)
	if check.Error != nil {
		return fmt.Errorf("VPC ACL 规则校验失败: %s", check.Stderr)
	}
	table := config.GlobalConfig.VPCACLTable
	if table == "" {
		table = "kvm_console_vpc_acl"
	}
	result := utils.ExecShell(fmt.Sprintf("nft delete table inet %s 2>/dev/null || true; nft -f %s", utils.ShellSingleQuote(table), utils.ShellSingleQuote(path)))
	if result.Error != nil {
		return fmt.Errorf("应用 VPC ACL 失败: %s", result.Stderr)
	}
	RemoveVPCPortForwardAcceptRules()
	_ = SavePortForwardRules()
	return nil
}

func aggregateSwitchMonthlyTrafficRaw(switchID uint) (downBytes, upBytes int64) {
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return 0, 0
	}
	vmNames := listVPCSwitchVMNames(sw)
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	monthEnd := monthStart.AddDate(0, 1, 0)
	for _, vmName := range vmNames {
		var records []model.VmStatsRecord
		model.DB.Where("vm_name = ? AND recorded_at >= ? AND recorded_at < ?", vmName, monthStart, monthEnd).
			Order("recorded_at ASC").Find(&records)
		for i := 1; i < len(records); i++ {
			if delta := records[i].NetRxBytes - records[i-1].NetRxBytes; delta > 0 {
				downBytes += delta
			}
			if delta := records[i].NetTxBytes - records[i-1].NetTxBytes; delta > 0 {
				upBytes += delta
			}
		}
	}
	return downBytes, upBytes
}

func AggregateSwitchMonthlyTraffic(switchID uint) (downBytes, upBytes int64) {
	rawDown, rawUp := aggregateSwitchMonthlyTrafficRaw(switchID)
	var record model.VPCSwitchTrafficMonthly
	if err := model.DB.Where("switch_id = ? AND month = ?", switchID, currentTrafficMonth()).First(&record).Error; err != nil {
		return rawDown, rawUp
	}
	return clampTrafficBytes(rawDown - record.OffsetDown), clampTrafficBytes(rawUp - record.OffsetUp)
}

func clampTrafficBytes(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func trafficQuotaBytes(gb float64) float64 {
	return gb * 1024 * 1024 * 1024
}

func getOrCreateVPCSwitchTrafficMonthly(sw model.VPCSwitch, month string) model.VPCSwitchTrafficMonthly {
	var record model.VPCSwitchTrafficMonthly
	if err := model.DB.Where("switch_id = ? AND month = ?", sw.ID, month).First(&record).Error; err == nil {
		return record
	}
	return model.VPCSwitchTrafficMonthly{
		SwitchID: sw.ID,
		Username: sw.Username,
		Month:    month,
	}
}

func saveVPCSwitchTrafficMonthly(record model.VPCSwitchTrafficMonthly) error {
	if record.ID == 0 {
		return model.DB.Create(&record).Error
	}
	return model.DB.Save(&record).Error
}

func rebaseVPCSwitchTrafficMonthly(switchID uint, keepDown, keepUp int64) {
	if model.DB == nil || switchID == 0 {
		return
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return
	}
	rawDown, rawUp := aggregateSwitchMonthlyTrafficRaw(switchID)
	record := getOrCreateVPCSwitchTrafficMonthly(sw, currentTrafficMonth())
	record.Username = sw.Username
	record.OffsetDown = rawDown - clampTrafficBytes(keepDown)
	record.OffsetUp = rawUp - clampTrafficBytes(keepUp)
	record.TrafficDown = clampTrafficBytes(keepDown)
	record.TrafficUp = clampTrafficBytes(keepUp)
	record.IsLimitedDown = sw.TrafficDownGB > 0 && float64(record.TrafficDown) >= trafficQuotaBytes(sw.TrafficDownGB)
	record.IsLimitedUp = sw.TrafficUpGB > 0 && float64(record.TrafficUp) >= trafficQuotaBytes(sw.TrafficUpGB)
	if err := saveVPCSwitchTrafficMonthly(record); err != nil {
		log.Printf("[VPC 流量配额] 重算交换机 %s(%d) 月流量偏移失败: %v", sw.Name, sw.ID, err)
	}
}

func effectiveVPCSwitchBandwidth(sw model.VPCSwitch) (downMbps, upMbps int) {
	normalizeVPCSwitchBandwidthForResponse(&sw)
	downMbps = sw.BandwidthDownMbps
	upMbps = sw.BandwidthUpMbps

	globalDown, globalUp := getGlobalEffectiveBandwidth()
	if globalDown > 0 && (downMbps <= 0 || downMbps > globalDown) {
		downMbps = globalDown
	}
	if globalUp > 0 && (upMbps <= 0 || upMbps > globalUp) {
		upMbps = globalUp
	}

	if downLimited, upLimited := IsVPCSwitchTrafficLimited(sw.ID); downLimited || upLimited {
		if downLimited {
			downMbps = vpcSwitchTrafficPenaltyMbps
		}
		if upLimited {
			upMbps = vpcSwitchTrafficPenaltyMbps
		}
	}
	return downMbps, upMbps
}

func IsVPCSwitchTrafficLimited(switchID uint) (downLimited, upLimited bool) {
	if model.DB == nil || switchID == 0 {
		return false, false
	}
	var record model.VPCSwitchTrafficMonthly
	if err := model.DB.Where("switch_id = ? AND month = ?", switchID, currentTrafficMonth()).First(&record).Error; err != nil {
		return false, false
	}
	return record.IsLimitedDown, record.IsLimitedUp
}

func CheckAndApplyVPCSwitchTrafficLimit(sw model.VPCSwitch) {
	if sw.ID == 0 || model.DB == nil {
		return
	}
	rawDown, rawUp := aggregateSwitchMonthlyTrafficRaw(sw.ID)
	record := getOrCreateVPCSwitchTrafficMonthly(sw, currentTrafficMonth())
	effectiveDown := clampTrafficBytes(rawDown - record.OffsetDown)
	effectiveUp := clampTrafficBytes(rawUp - record.OffsetUp)
	record.Username = sw.Username
	record.TrafficDown = effectiveDown
	record.TrafficUp = effectiveUp

	downLimited := sw.TrafficDownGB > 0 && float64(effectiveDown) >= trafficQuotaBytes(sw.TrafficDownGB)
	upLimited := sw.TrafficUpGB > 0 && float64(effectiveUp) >= trafficQuotaBytes(sw.TrafficUpGB)
	wasLimited := record.IsLimitedDown || record.IsLimitedUp
	changed := record.IsLimitedDown != downLimited || record.IsLimitedUp != upLimited
	record.IsLimitedDown = downLimited
	record.IsLimitedUp = upLimited

	if err := saveVPCSwitchTrafficMonthly(record); err != nil {
		log.Printf("[VPC 流量配额] 保存交换机 %s(%d) 月流量失败: %v", sw.Name, sw.ID, err)
		return
	}
	if changed || wasLimited != (downLimited || upLimited) {
		if err := applyVPCSwitchBandwidth(sw); err != nil {
			log.Printf("[VPC 流量配额] 应用交换机 %s(%d) 限速状态失败: %v", sw.Name, sw.ID, err)
		}
	}
	if (downLimited || upLimited) && changed {
		log.Printf("[VPC 流量配额] 交换机 %s(%d) 本月流量超限，已按超限方向强制限速 %dMbps（下行 %s / %.2fGB，上行 %s / %.2fGB）",
			sw.Name, sw.ID, vpcSwitchTrafficPenaltyMbps, formatTrafficBytes(effectiveDown), sw.TrafficDownGB, formatTrafficBytes(effectiveUp), sw.TrafficUpGB)
	}
}

func CheckAllVPCSwitchTrafficQuota() {
	if model.DB == nil {
		return
	}
	var switches []model.VPCSwitch
	model.DB.Find(&switches)
	for _, sw := range switches {
		CheckAndApplyVPCSwitchTrafficLimit(sw)
	}
}

func CheckVPCSwitchTrafficAfterQuotaUpdate(switchID uint) {
	if switchID == 0 || model.DB == nil {
		return
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return
	}
	rawDown, rawUp := aggregateSwitchMonthlyTrafficRaw(switchID)
	record := getOrCreateVPCSwitchTrafficMonthly(sw, currentTrafficMonth())
	effectiveDown := clampTrafficBytes(rawDown - record.OffsetDown)
	effectiveUp := clampTrafficBytes(rawUp - record.OffsetUp)
	record.TrafficDown = effectiveDown
	record.TrafficUp = effectiveUp
	if !record.IsLimitedDown && !record.IsLimitedUp {
		_ = saveVPCSwitchTrafficMonthly(record)
		return
	}
	downLimited := sw.TrafficDownGB > 0 && float64(effectiveDown) >= trafficQuotaBytes(sw.TrafficDownGB)
	upLimited := sw.TrafficUpGB > 0 && float64(effectiveUp) >= trafficQuotaBytes(sw.TrafficUpGB)
	if record.IsLimitedDown == downLimited && record.IsLimitedUp == upLimited {
		_ = saveVPCSwitchTrafficMonthly(record)
		return
	}
	record.IsLimitedDown = downLimited
	record.IsLimitedUp = upLimited
	if err := saveVPCSwitchTrafficMonthly(record); err != nil {
		log.Printf("[VPC 流量配额] 保存交换机 %s(%d) 解限状态失败: %v", sw.Name, sw.ID, err)
		return
	}
	if !downLimited && !upLimited {
		log.Printf("[VPC 流量配额] 交换机 %s(%d) 配额调整后已低于用量，解除强制限速", sw.Name, sw.ID)
	}
	if err := applyVPCSwitchBandwidth(sw); err != nil {
		log.Printf("[VPC 流量配额] 配额调整后应用交换机 %s(%d) 带宽失败: %v", sw.Name, sw.ID, err)
	}
}

func ResetAllVPCSwitchMonthlyTraffic() {
	if model.DB == nil {
		return
	}
	lastMonth := time.Now().AddDate(0, -1, 0).Format("2006-01")
	var records []model.VPCSwitchTrafficMonthly
	model.DB.Where("month = ? AND (is_limited_down = ? OR is_limited_up = ?)", lastMonth, true, true).Find(&records)
	for _, record := range records {
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, record.SwitchID).Error; err != nil {
			continue
		}
		if err := applyVPCSwitchBandwidth(sw); err != nil {
			log.Printf("[VPC 流量配额] 月重置后恢复交换机 %s(%d) 带宽失败: %v", sw.Name, sw.ID, err)
		}
	}
	cleanupMonth := time.Now().AddDate(0, -12, 0).Format("2006-01")
	model.DB.Where("month < ?", cleanupMonth).Delete(&model.VPCSwitchTrafficMonthly{})
}

// ==================== 多网口管理（仅管理员） ====================

// AddVMInterfaceRequest 添加虚拟机网口的请求参数
type AddVMInterfaceRequest struct {
	SwitchID        uint   `json:"switch_id"`
	SecurityGroupID uint   `json:"security_group_id"`
	NicModel        string `json:"nic_model"`
}

// VMInterfaceInfo 网口信息
type VMInterfaceInfo struct {
	Binding       model.VPCVMBinding     `json:"binding"`
	Switch        *model.VPCSwitch       `json:"switch"`
	SecurityGroup *model.VPCSecurityGroup `json:"security_group"`
}

// AddVMInterface 为虚拟机新增一个网口并绑定到 VPC 交换机（仅管理员）
func AddVMInterface(vmName string, req AddVMInterfaceRequest) (*VMInterfaceInfo, error) {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return nil, fmt.Errorf("虚拟机名称不能为空")
	}

	// 验证交换机存在
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, req.SwitchID).Error; err != nil {
		return nil, fmt.Errorf("交换机不存在")
	}

	// 安全组处理
	securityGroupID := req.SecurityGroupID
	if !SwitchUsesDirectBridge(sw) {
		if securityGroupID == 0 {
			if _, err := EnsureDefaultSecurityGroup(sw.Username); err != nil {
				return nil, err
			}
			var group model.VPCSecurityGroup
			if err := model.DB.Where("username = ? AND is_default = ?", sw.Username, true).First(&group).Error; err != nil {
				return nil, fmt.Errorf("未找到交换机用户 %s 的默认安全组", sw.Username)
			}
			securityGroupID = group.ID
		} else {
			var group model.VPCSecurityGroup
			if err := model.DB.First(&group, securityGroupID).Error; err != nil {
				return nil, fmt.Errorf("安全组不存在")
			}
			if group.Username != sw.Username {
				return nil, fmt.Errorf("安全组必须属于交换机用户 %s", sw.Username)
			}
		}
	}

	// 确定下一个 interface_order
	var maxOrder int
	if err := model.DB.Model(&model.VPCVMBinding{}).
		Where("vm_name = ?", vmName).
		Select("COALESCE(MAX(interface_order), -1) as max_order").
		Scan(&maxOrder).Error; err != nil {
		return nil, fmt.Errorf("查询现有网口失败: %w", err)
	}
	nextOrder := maxOrder + 1

	// 网卡型号
	nicModel := strings.TrimSpace(req.NicModel)
	if nicModel == "" {
		nicModel = "virtio"
	}

	// 确保交换机运行时已就绪
	if err := EnsureVPCSwitchRuntime(sw); err != nil {
		return nil, err
	}

	// 创建 VM 网口 XML 并附加到虚拟机
	if err := attachVMInterface(vmName, sw, nicModel, nextOrder); err != nil {
		return nil, err
	}

	// 如果 nextOrder == 0 表示没有现有绑定，需要检查是否已有默认绑定（旧数据迁移场景）
	if nextOrder == 0 {
		var existingCount int64
		model.DB.Model(&model.VPCVMBinding{}).Where("vm_name = ?", vmName).Count(&existingCount)
		if existingCount > 0 {
			// 已有绑定存在但没找到 maxOrder（理论上不会发生），重新查询
			var newMax int
			model.DB.Model(&model.VPCVMBinding{}).
				Where("vm_name = ?", vmName).
				Select("COALESCE(MAX(interface_order), 0) as max_order").
				Scan(&newMax)
			nextOrder = newMax + 1
		}
	}

	// 创建 VPC 绑定记录
	binding := model.VPCVMBinding{
		VMName:          vmName,
		Username:        sw.Username,
		SwitchID:        req.SwitchID,
		SecurityGroupID: securityGroupID,
		InterfaceOrder:  nextOrder,
		NicModel:        nicModel,
	}
	if err := model.DB.Create(&binding).Error; err != nil {
		return nil, fmt.Errorf("创建网口绑定记录失败: %w", err)
	}

	// 应用新网口的 VPC 运行态（只处理新接口，不影响已有接口）
	if err := applyNewInterfaceRuntime(vmName, sw, nextOrder); err != nil {
		fmt.Printf("[警告] 为新网口 %s #%d 应用 VPC 运行态失败: %v\n", vmName, nextOrder, err)
	}
	// 仅刷新交换机带宽和 ACL，不修改已有网口
	if err := applyVPCSwitchBandwidth(sw); err != nil {
		fmt.Printf("[警告] 刷新交换机 %s 带宽失败: %v\n", sw.Name, err)
	}
	if !SwitchUsesDirectBridge(sw) {
		_ = ApplyVPCACLRules()
	}

	return &VMInterfaceInfo{
		Binding:       binding,
		Switch:        &sw,
		SecurityGroup: nil,
	}, nil
}

// RemoveVMInterface 删除虚拟机的指定网口（仅管理员）
func RemoveVMInterface(vmName string, interfaceOrder int) error {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return fmt.Errorf("虚拟机名称不能为空")
	}

	if interfaceOrder == 0 {
		return fmt.Errorf("不能删除主网口（接口序号 0），请先确保有其他网口存在或直接删除虚拟机")
	}

	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ? AND interface_order = ?", vmName, interfaceOrder).First(&binding).Error; err != nil {
		return fmt.Errorf("未找到指定的网口绑定")
	}

	// 从虚拟机 XML 中移除网口
	if err := detachVMInterface(vmName, interfaceOrder); err != nil {
		return err
	}

	// 删除绑定记录
	switchID := binding.SwitchID
	if err := model.DB.Delete(&binding).Error; err != nil {
		return fmt.Errorf("删除网口绑定记录失败: %w", err)
	}

	// 刷新交换机带宽和 ACL
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err == nil {
		_ = applyVPCSwitchBandwidth(sw)
		if !SwitchUsesDirectBridge(sw) {
			_ = ApplyVPCACLRules()
		}
	}

	return nil
}

// AttachExtraNICs 批量附加额外网口（用于创建/克隆流程）
func AttachExtraNICs(vmName string, extraNics []AddVMInterfaceRequest) {
	for i, nic := range extraNics {
		if nic.SwitchID == 0 {
			continue
		}
		if _, err := AddVMInterface(vmName, nic); err != nil {
			log.Printf("[警告] 为 VM %s 添加额外网口 #%d (交换机 %d) 失败: %v\n", vmName, i+1, nic.SwitchID, err)
		}
	}
}

// applyNewInterfaceRuntime 为新添加的网口设置 OVS VLAN tag（不影响已有网口）
func applyNewInterfaceRuntime(vmName string, sw model.VPCSwitch, interfaceOrder int) error {
	state := strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout)
	if state != "running" {
		return nil // 关机态的 VLAN 已在 XML 中配置
	}

	// 从 domiflist 获取新网口的 vnet 接口名
	vnetIF := getVMVnetIFByOrder(vmName, interfaceOrder)
	if vnetIF == "" {
		// 等待 vnet 接口出现
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			vnetIF = getVMVnetIFByOrder(vmName, interfaceOrder)
			if vnetIF != "" {
				break
			}
		}
	}
	if vnetIF == "" {
		return fmt.Errorf("无法找到新网口对应的 vnet 接口")
	}

	if !SwitchUsesDirectBridge(sw) && sw.VLANID > 0 {
		targetTag := strconv.Itoa(sw.VLANID)
		result := utils.ExecCommand("ovs-vsctl", "set", "Port", vnetIF, "tag="+targetTag)
		if result.Error != nil {
			return fmt.Errorf("设置新网口 OVS VLAN tag 失败: %s", result.Stderr)
		}
	}
	// 清理该接口的旧 DHCP 租约
	mac := GetVMMACByOrder(vmName, interfaceOrder)
	if mac != "" {
		CleanOVSDHCPLease(mac, "")
	}
	return nil
}

// getVMVnetIFByOrder 获取虚拟机第 N 个网口对应的 vnet 接口名
func getVMVnetIFByOrder(vmName string, order int) string {
	result := utils.ExecCommand("virsh", "domiflist", vmName)
	if result.Error != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	idx := 0
	for i, line := range lines {
		if i < 2 || strings.TrimSpace(line) == "" {
			continue
		}
		if idx == order {
			fields := strings.Fields(line)
			if len(fields) >= 1 {
				return fields[0] // 第一列是 Interface 名称（如 vnet0）
			}
		}
		idx++
	}
	return ""
}

// ListVMInterfaces 列出虚拟机所有网口绑定
func ListVMInterfaces(vmName string) ([]VMInterfaceInfo, error) {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return nil, fmt.Errorf("虚拟机名称不能为空")
	}

	var bindings []model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).Order("interface_order ASC").Find(&bindings).Error; err != nil {
		return nil, err
	}

	result := make([]VMInterfaceInfo, 0, len(bindings))
	for _, b := range bindings {
		info := VMInterfaceInfo{Binding: b}
		var sw model.VPCSwitch
		if model.DB.First(&sw, b.SwitchID).Error == nil {
			normalizeVPCSwitchBandwidthForResponse(&sw)
			info.Switch = &sw
		}
		var sg model.VPCSecurityGroup
		if model.DB.First(&sg, b.SecurityGroupID).Error == nil {
			info.SecurityGroup = &sg
		}
		result = append(result, info)
	}
	return result, nil
}

func IPInCIDR(ipText, cidrText string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipText))
	_, network, err := net.ParseCIDR(strings.TrimSpace(cidrText))
	return ip != nil && err == nil && network.Contains(ip)
}

func IsVPCManagedIP(ipText string) bool {
	ipText = strings.TrimSpace(ipText)
	if ipText == "" || model.DB == nil {
		return false
	}
	var switches []model.VPCSwitch
	model.DB.Find(&switches)
	for _, sw := range switches {
		if IPInCIDR(ipText, sw.CIDR) {
			return true
		}
	}
	return false
}

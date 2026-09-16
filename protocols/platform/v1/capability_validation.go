package platformv1

// capability_validation.go：Gateway 能力声明的版本化准入。
//
// 兼容矩阵是编译进协议库的受控输入；Fleet、Gateway 注册工具和离线
// 验证使用同一份规则。未匹配矩阵的组合不能进入监控或控制面，匹配但
// 缺少车规安全硬件/链路的组合最多只能监控，绝不能被当成可接管车辆。

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

//go:embed compatibility_matrix.json
var compatibilityMatrixJSON []byte

type compatibilityMatrix struct {
	SchemaMajor uint32                     `json:"schema_major"`
	SchemaMinor uint32                     `json:"schema_minor"`
	Entries     []compatibilityMatrixEntry `json:"entries"`
}

type compatibilityMatrixEntry struct {
	ID                       string   `json:"id"`
	Stack                    string   `json:"stack"`
	StackVersion             string   `json:"stack_version"`
	GatewayVersion           string   `json:"gateway_version"`
	AdapterVersion           string   `json:"adapter_version"`
	SafetyArbiterVersion     string   `json:"safety_arbiter_version"`
	TopicMappingVersion      string   `json:"topic_mapping_version"`
	RequiredControlModes     []string `json:"required_control_modes"`
	RequiresCertificate      bool     `json:"requires_certificate"`
	RequiresTPMForControl    bool     `json:"requires_tpm_for_control"`
	RequiredModemsForControl int      `json:"required_modems_for_control"`
}

// CapabilityDecision is the auditable result of evaluating one declaration.
// MonitoringAllowed and ControlAllowed are intentionally separate: an
// installed but not hardware-complete Gateway may send health data while it
// remains ineligible for remote control.
type CapabilityDecision struct {
	MatrixEntry       string
	MonitoringAllowed bool
	ControlAllowed    bool
	ControlReason     string
}

func loadCompatibilityMatrix() (compatibilityMatrix, error) {
	var matrix compatibilityMatrix
	if err := json.Unmarshal(compatibilityMatrixJSON, &matrix); err != nil {
		return matrix, fmt.Errorf("解析兼容矩阵: %w", err)
	}
	if matrix.SchemaMajor != SupportedSchemaMajor || matrix.SchemaMinor != SupportedSchemaMinor || len(matrix.Entries) == 0 {
		return matrix, fmt.Errorf("兼容矩阵版本或条目无效")
	}
	for i, entry := range matrix.Entries {
		if entry.ID == "" || entry.Stack == "" || entry.StackVersion == "" || entry.GatewayVersion == "" ||
			entry.AdapterVersion == "" || entry.SafetyArbiterVersion == "" || entry.TopicMappingVersion == "" ||
			entry.RequiredModemsForControl < 0 {
			return matrix, fmt.Errorf("兼容矩阵第 %d 项字段不完整", i)
		}
	}
	return matrix, nil
}

// AssessGatewayCapabilities validates required identity/version fields and
// returns a fail-closed control decision. It does not perform certificate
// authentication; the transport and registry must do that separately.
func AssessGatewayCapabilities(caps *GatewayCapabilities) (CapabilityDecision, error) {
	decision := CapabilityDecision{}
	if caps == nil {
		return decision, fmt.Errorf("GatewayCapabilities 为空")
	}
	if strings.TrimSpace(caps.GetVehicleId()) == "" ||
		strings.TrimSpace(caps.GetGatewayVersion()) == "" ||
		strings.TrimSpace(caps.GetStackVersion()) == "" ||
		strings.TrimSpace(caps.GetTopicMappingVersion()) == "" ||
		strings.TrimSpace(caps.GetAdapterVersion()) == "" ||
		strings.TrimSpace(caps.GetSafetyArbiterVersion()) == "" ||
		caps.GetStack() == AutonomyStack_AUTONOMY_STACK_UNSPECIFIED {
		return decision, fmt.Errorf("GatewayCapabilities 缺少版本、身份或栈类型")
	}
	if len(caps.GetSupportedControlModes()) == 0 {
		return decision, fmt.Errorf("GatewayCapabilities 未声明控制模式")
	}
	if len(caps.GetSupportedControlModes()) > 16 || len(caps.GetModems()) > 8 || len(caps.GetCameras()) > 64 || len(caps.GetWorkspaceFeatures()) > 64 {
		return decision, fmt.Errorf("GatewayCapabilities 列表数量超过安全上限")
	}
	seenModes := make(map[ControlMode]struct{}, len(caps.GetSupportedControlModes()))
	for _, mode := range caps.GetSupportedControlModes() {
		switch mode {
		case ControlMode_CONTROL_MODE_DIRECT_ACTUATION, ControlMode_CONTROL_MODE_TARGET_MOTION,
			ControlMode_CONTROL_MODE_TRAJECTORY, ControlMode_CONTROL_MODE_MINIMAL_RISK:
		default:
			return decision, fmt.Errorf("GatewayCapabilities 包含未知控制模式 %d", mode)
		}
		if _, exists := seenModes[mode]; exists {
			return decision, fmt.Errorf("GatewayCapabilities 包含重复控制模式")
		}
		seenModes[mode] = struct{}{}
	}
	seenCameras := make(map[string]struct{}, len(caps.GetCameras()))
	for _, camera := range caps.GetCameras() {
		if camera == nil || strings.TrimSpace(camera.GetCameraId()) == "" || camera.GetWidth() == 0 || camera.GetHeight() == 0 ||
			camera.GetWidth() > 16384 || camera.GetHeight() > 16384 || camera.GetFps() == 0 || camera.GetFps() > 240 ||
			strings.TrimSpace(camera.GetCodec()) == "" || camera.GetMaxBitrateKbps() == 0 {
			return decision, fmt.Errorf("GatewayCapabilities 包含无效 Camera")
		}
		if _, exists := seenCameras[camera.GetCameraId()]; exists {
			return decision, fmt.Errorf("GatewayCapabilities 包含重复 Camera ID")
		}
		seenCameras[camera.GetCameraId()] = struct{}{}
	}
	seenModems := make(map[string]struct{}, len(caps.GetModems()))
	for _, modem := range caps.GetModems() {
		if modem == nil || strings.TrimSpace(modem.GetModemId()) == "" {
			return decision, fmt.Errorf("GatewayCapabilities 包含无效 Modem")
		}
		if _, exists := seenModems[modem.GetModemId()]; exists {
			return decision, fmt.Errorf("GatewayCapabilities 包含重复 Modem ID")
		}
		seenModems[modem.GetModemId()] = struct{}{}
	}
	matrix, err := loadCompatibilityMatrix()
	if err != nil {
		return decision, err
	}
	stack := caps.GetStack().String()
	for _, entry := range matrix.Entries {
		if entry.Stack != stack || entry.StackVersion != caps.GetStackVersion() ||
			entry.GatewayVersion != caps.GetGatewayVersion() || entry.AdapterVersion != caps.GetAdapterVersion() ||
			entry.SafetyArbiterVersion != caps.GetSafetyArbiterVersion() || entry.TopicMappingVersion != caps.GetTopicMappingVersion() {
			continue
		}
		decision.MatrixEntry = entry.ID
		decision.MonitoringAllowed = true
		decision.ControlAllowed = true
		var reasons []string
		if entry.RequiresCertificate && !caps.GetCertificateInstalled() {
			decision.MonitoringAllowed = false
			decision.ControlAllowed = false
			reasons = append(reasons, "Gateway 未声明已安装证书")
		}
		modeSet := make(map[string]struct{}, len(caps.GetSupportedControlModes()))
		for _, mode := range caps.GetSupportedControlModes() {
			modeSet[mode.String()] = struct{}{}
		}
		for _, required := range entry.RequiredControlModes {
			if _, ok := modeSet[required]; !ok {
				decision.ControlAllowed = false
				reasons = append(reasons, "未声明矩阵要求的控制模式 "+required)
			}
		}
		if entry.RequiresTPMForControl && !caps.GetTpmAvailable() {
			decision.ControlAllowed = false
			reasons = append(reasons, "未提供 TPM/设备证明能力")
		}
		if len(caps.GetModems()) < entry.RequiredModemsForControl {
			decision.ControlAllowed = false
			reasons = append(reasons, fmt.Sprintf("独立 Modem 数量不足（需要 %d）", entry.RequiredModemsForControl))
		}
		if !decision.MonitoringAllowed {
			decision.ControlReason = strings.Join(reasons, "；")
		} else if decision.ControlAllowed {
			decision.ControlReason = "兼容矩阵匹配，控制能力完整"
		} else {
			decision.ControlReason = strings.Join(reasons, "；")
		}
		return decision, nil
	}
	decision.ControlReason = "未匹配受控 Gateway/Adapter/Safety Arbiter 兼容矩阵"
	return decision, nil
}

// ValidateSignalUpdate validates semantic telemetry fields that proto3 cannot
// express as required. A fresh Envelope alone is not enough: an empty or
// malformed SignalUpdate must never make a vehicle appear online.
func ValidateSignalUpdate(update *SignalUpdate, now time.Time) error {
	if update == nil || len(update.GetSignals()) == 0 || len(update.GetSignals()) > 512 {
		return fmt.Errorf("SignalUpdate 信号数量无效")
	}
	if now.IsZero() {
		now = time.Now()
	}
	seen := make(map[string]struct{}, len(update.GetSignals()))
	for _, signal := range update.GetSignals() {
		if signal == nil || strings.TrimSpace(signal.GetPath()) == "" || len(signal.GetPath()) > 256 ||
			strings.IndexFunc(signal.GetPath(), unicode.IsControl) >= 0 {
			return fmt.Errorf("Signal 路径无效")
		}
		if _, exists := seen[signal.GetPath()]; exists {
			return fmt.Errorf("SignalUpdate 包含重复路径 %q", signal.GetPath())
		}
		seen[signal.GetPath()] = struct{}{}
		switch signal.GetQuality() {
		case SignalQuality_SIGNAL_QUALITY_GOOD, SignalQuality_SIGNAL_QUALITY_STALE,
			SignalQuality_SIGNAL_QUALITY_MISSING, SignalQuality_SIGNAL_QUALITY_INVALID:
		default:
			return fmt.Errorf("Signal 质量枚举无效")
		}
		if signal.GetSampleMonotonicNs() == 0 || signal.GetSampleUtcNs() <= 0 {
			return fmt.Errorf("Signal 缺少采样时间")
		}
		sampleTime := time.Unix(0, signal.GetSampleUtcNs())
		if sampleTime.After(now.Add(2 * time.Second)) || sampleTime.Before(now.Add(-24*time.Hour)) {
			return fmt.Errorf("Signal 采样时间超出允许窗口")
		}
		value := signal.GetValue()
		if value == nil {
			if signal.GetQuality() == SignalQuality_SIGNAL_QUALITY_GOOD || signal.GetQuality() == SignalQuality_SIGNAL_QUALITY_STALE {
				return fmt.Errorf("有效 Signal 缺少 value")
			}
			continue
		}
		switch typed := value.GetKind().(type) {
		case *Value_Number:
			if math.IsNaN(typed.Number) || math.IsInf(typed.Number, 0) {
				return fmt.Errorf("Signal 数值不能为 NaN 或 Inf")
			}
		case *Value_Boolean, *Value_Text, *Value_Raw:
		case *Value_NumberList:
			if typed.NumberList == nil || len(typed.NumberList.GetValues()) > 256 {
				return fmt.Errorf("Signal 数组值超过上限")
			}
			for _, number := range typed.NumberList.GetValues() {
				if math.IsNaN(number) || math.IsInf(number, 0) {
					return fmt.Errorf("Signal 数组数值不能为 NaN 或 Inf")
				}
			}
		default:
			return fmt.Errorf("Signal value 类型无效")
		}
		if raw := value.GetRaw(); len(raw) > 64*1024 {
			return fmt.Errorf("Signal raw 值超过上限")
		}
	}
	return nil
}

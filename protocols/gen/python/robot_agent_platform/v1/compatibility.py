"""Repository compatibility-matrix evaluator for Python vehicle components.

The matrix is the same JSON artifact embedded by the Go protocol package. A
Gateway may continue in monitoring-only mode when control hardware is absent,
but an unknown software combination cannot enter either runtime data plane.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from . import capabilities_pb2, control_pb2


@dataclass(frozen=True)
class CapabilityDecision:
    matrix_entry: str
    monitoring_allowed: bool
    control_allowed: bool
    control_reason: str


def _matrix_path() -> Path:
    # .../protocols/gen/python/robot_agent_platform/v1 -> repository root.
    return Path(__file__).resolve().parents[5] / "protocols" / "platform" / "v1" / "compatibility_matrix.json"


def _load_matrix(path: Path | None = None) -> dict:
    matrix_file = _matrix_path() if path is None else Path(path)
    try:
        matrix = json.loads(matrix_file.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise ValueError(f"无法读取兼容矩阵: {exc}") from exc
    if matrix.get("schema_major") != 1 or matrix.get("schema_minor") != 0 or not matrix.get("entries"):
        raise ValueError("兼容矩阵版本或条目无效")
    return matrix


def assess_gateway_capabilities(
    caps: capabilities_pb2.GatewayCapabilities, *, matrix_path: Path | None = None
) -> CapabilityDecision:
    if not isinstance(caps, capabilities_pb2.GatewayCapabilities):
        raise ValueError("GatewayCapabilities 类型无效")
    required = (
        caps.vehicle_id,
        caps.gateway_version,
        caps.stack_version,
        caps.topic_mapping_version,
        caps.adapter_version,
        caps.safety_arbiter_version,
    )
    if not all(value.strip() for value in required) or caps.stack == capabilities_pb2.AUTONOMY_STACK_UNSPECIFIED:
        raise ValueError("GatewayCapabilities 缺少版本、身份或栈类型")
    if not 0 < len(caps.supported_control_modes) <= 16 or len(caps.modems) > 8 or len(caps.cameras) > 64:
        raise ValueError("GatewayCapabilities 列表数量超过安全上限")
    valid_modes = {
        control_pb2.CONTROL_MODE_DIRECT_ACTUATION,
        control_pb2.CONTROL_MODE_TARGET_MOTION,
        control_pb2.CONTROL_MODE_TRAJECTORY,
        control_pb2.CONTROL_MODE_MINIMAL_RISK,
    }
    if any(mode not in valid_modes for mode in caps.supported_control_modes):
        raise ValueError("GatewayCapabilities 包含未知控制模式")
    if len(set(caps.supported_control_modes)) != len(caps.supported_control_modes):
        raise ValueError("GatewayCapabilities 包含重复控制模式")
    modem_ids = [modem.modem_id.strip() for modem in caps.modems]
    if any(not modem_id for modem_id in modem_ids) or len(set(modem_ids)) != len(modem_ids):
        raise ValueError("GatewayCapabilities 包含无效或重复 Modem ID")
    camera_ids = [camera.camera_id.strip() for camera in caps.cameras]
    if any(
        not camera_id or camera.width == 0 or camera.height == 0 or camera.width > 16384 or camera.height > 16384
        or camera.fps == 0 or camera.fps > 240 or not camera.codec.strip() or camera.max_bitrate_kbps == 0
        for camera, camera_id in zip(caps.cameras, camera_ids)
    ) or len(set(camera_ids)) != len(camera_ids):
        raise ValueError("GatewayCapabilities 包含无效或重复 Camera")

    stack_name = capabilities_pb2.AutonomyStack.Name(caps.stack)
    for entry in _load_matrix(matrix_path)["entries"]:
        if (
            entry.get("stack") != stack_name
            or entry.get("stack_version") != caps.stack_version
            or entry.get("gateway_version") != caps.gateway_version
            or entry.get("adapter_version") != caps.adapter_version
            or entry.get("safety_arbiter_version") != caps.safety_arbiter_version
            or entry.get("topic_mapping_version") != caps.topic_mapping_version
        ):
            continue
        reasons: list[str] = []
        monitoring_allowed = True
        control_allowed = True
        if entry.get("requires_certificate", False) and not caps.certificate_installed:
            monitoring_allowed = False
            control_allowed = False
            reasons.append("Gateway 未声明已安装证书")
        mode_names = {control_pb2.ControlMode.Name(mode) for mode in caps.supported_control_modes}
        for required_mode in entry.get("required_control_modes", []):
            if required_mode not in mode_names:
                control_allowed = False
                reasons.append(f"未声明矩阵要求的控制模式 {required_mode}")
        if entry.get("requires_tpm_for_control", False) and not caps.tpm_available:
            control_allowed = False
            reasons.append("未提供 TPM/设备证明能力")
        required_modems = int(entry.get("required_modems_for_control", 0))
        if len(caps.modems) < required_modems:
            control_allowed = False
            reasons.append(f"独立 Modem 数量不足（需要 {required_modems}）")
        return CapabilityDecision(
            matrix_entry=entry["id"],
            monitoring_allowed=monitoring_allowed,
            control_allowed=control_allowed,
            control_reason="兼容矩阵匹配，控制能力完整" if control_allowed else "；".join(reasons),
        )
    return CapabilityDecision(
        matrix_entry="",
        monitoring_allowed=False,
        control_allowed=False,
        control_reason="未匹配受控 Gateway/Adapter/Safety Arbiter 兼容矩阵",
    )

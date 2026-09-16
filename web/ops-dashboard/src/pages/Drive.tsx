// 远程接管：接管设备绑定页（列表式）。
// 交互：点「绑定设备」→ 弹窗 1 左右滑动选择设备类型 → 弹窗 2 填写该类型绑定信息
// （一体机 SN+密码 / 遥控器·方向盘 USB 识别 / 键盘一键）→ 绑定成功回到列表。
// 列表中点「使用」→ 可接管车辆弹窗（一账号一次一辆）→ 进入驾驶页。
import { useCallback, useEffect, useState } from "react";
import Modal from "../components/Modal";
import type { FleetState } from "../api";
import { bindDevice, fetchDevices, unbindDevice } from "../api";
import type { DeviceInfo, DeviceType, Me } from "../types";
import { DEVICE_TYPE_LABEL, useGamepads, type GamepadInfo } from "../peripherals";
import VehiclePickerModal from "../components/VehiclePickerModal";
import "./drive.css";

interface Props {
  fleet: FleetState;
  me: Me;
}

const TYPES: { id: DeviceType; letter: string; label: string; sub: string; img: string }[] = [
  { id: "console", letter: "A", label: "自研一体机", sub: "SN 码 + 密码准入，需受信控制代理在线注册后才能用于接管", img: "img/dev-console.png" },
  { id: "rc", letter: "B", label: "航模遥控器 (USB)", sub: "USB 设备可被浏览器检测；须经受信控制代理确认后才能用于接管", img: "img/dev-rc.png" },
  { id: "keyboard", letter: "C", label: "键盘控制", sub: "仅登记键盘输入准入；未建立受信控制通道时不能下发车辆控制", img: "img/dev-keyboard.png" },
  { id: "wheel", letter: "D", label: "罗技方向盘", sub: "USB 设备可被浏览器检测；须经受信控制代理确认后才能用于接管", img: "img/dev-wheel.png" },
];

const HashIcon = () => (
  <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M4 9h16M4 15h16M10 3 8 21M16 3l-2 18" />
  </svg>
);
const LockIcon = () => (
  <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <rect x="4" y="11" width="16" height="10" rx="2" />
    <path d="M8 11V7a4 4 0 0 1 8 0v4" />
  </svg>
);
const KbIcon = () => (
  <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <rect x="2" y="6" width="20" height="12" rx="2" />
    <path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M6 14h.01M18 14h.01M9 14h6" />
  </svg>
);
const PlusIcon = () => (
  <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round">
    <path d="M12 5v14M5 12h14" />
  </svg>
);

export default function Drive({ fleet, me }: Props) {
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [loaded, setLoaded] = useState(false);
  const pads = useGamepads();
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [typePickerOpen, setTypePickerOpen] = useState(false);
  const [bindType, setBindType] = useState<DeviceType | null>(null);
  const [pickerDevice, setPickerDevice] = useState<DeviceInfo | null>(null);
  // 一体机表单
  const [consoleSN, setConsoleSN] = useState("");
  const [consolePwd, setConsolePwd] = useState("");

  const reload = useCallback(() => {
    void fetchDevices()
      .then((d) => {
        setDevices(d);
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
  }, []);

  useEffect(() => {
    reload();
  }, [reload]);

  const say = (ok: boolean, text: string) => setMsg({ ok, text });

  const doBind = async (body: {
    type: string;
    name: string;
    link?: string;
    sn?: string;
    password?: string;
  }): Promise<boolean> => {
    setBusy(true);
    setMsg(null);
    try {
      const r = await bindDevice(body);
      if (r.ok) {
        say(true, (DEVICE_TYPE_LABEL[body.type] || body.type) + " 绑定成功，点「使用」选择要接管的车辆");
        setBindType(null);
        setConsoleSN("");
        setConsolePwd("");
        reload();
        return true;
      }
      say(false, "绑定失败：" + String(r.error || "未知错误"));
    } catch (e) {
      say(false, "绑定失败：" + String(e));
    } finally {
      setBusy(false);
    }
    return false;
  };

  const doUnbind = async (d: DeviceInfo) => {
    if (!window.confirm("确认解绑「" + d.name + "」？")) return;
    try {
      await unbindDevice(d.id);
      say(true, "已解绑「" + d.name + "」");
    } catch (e) {
      say(false, "解绑失败：" + String(e));
    }
    reload();
  };

  const padIsBound = (p: GamepadInfo) =>
    devices.some((d) => (d.type === "rc" || d.type === "wheel") && d.name === p.id);

  // 浏览器是否看见 USB 设备不是工业控制通道在线证明。
  // “可接管”只认后端受信控制代理上报的 device.online。
  const liveStatus = (d: DeviceInfo): { on: boolean; text: string } => {
    return { on: d.online, text: d.online ? "控制代理已验证" : "等待控制代理注册" };
  };

  // 顶部外设状态 pills
  const pills: { cls: string; text: string }[] = [];
  for (const device of devices.filter((d) => d.online)) pills.push({ cls: "on", text: (DEVICE_TYPE_LABEL[device.type] || device.type) + " · 已验证" });
  if (pads.length > 0) pills.push({ cls: "dim", text: "浏览器已检测 USB（未作控制授权）" });
  if (pads.length === 0) pills.push({ cls: "dim", text: "USB 外设未检测到" });

  const bindModalTitle = bindType ? "绑定" + (TYPES.find((t) => t.id === bindType)?.label || "") : "";

  return (
    <div className="dv-root">
      <div className="dv-head">
        <div className="dv-head-left">
          <h2>
            接管设备绑定
            <span className="dv-dash" />
          </h2>
          <div className="dv-sub">
            {me.display_name || me.username}
            ，设备绑定后还须由受信控制代理完成在线注册，平台才允许选择车辆发起接管。
          </div>
        </div>
        <div className="dv-peri-strip">
          <span className="dv-strip-title">外设连接状态</span>
          {pills.map((p, i) => (
            <span key={i} className={"dv-pill " + p.cls}>{p.text}</span>
          ))}
        </div>
      </div>

      {msg && (
        <div className={"dv-banner" + (msg.ok ? " ok" : " err")}>
          <span className="dv-banner-ic">{msg.ok ? "✓" : "!"}</span>
          <span>{msg.text}</span>
          <button className="dv-banner-x" onClick={() => setMsg(null)} title="关闭">✕</button>
        </div>
      )}

      <div className="dv-toolbar">
        <span className="dv-list-title">已绑定设备（{devices.length}）</span>
        <button className="dv-bind-btn blue sm" onClick={() => setTypePickerOpen(true)}>
          <PlusIcon /> 绑定设备
        </button>
      </div>

      <div className="dv-list">
        {devices.map((d) => {
          const st = liveStatus(d);
          const t = TYPES.find((x) => x.id === d.type);
          return (
            <div className="dv-row" key={d.id}>
              <span className={"dv-letter " + (t ? t.letter.toLowerCase() : "a")}>{t ? t.letter : "•"}</span>
              <div className="dv-row-main">
                <span className="dv-row-name">{d.name}</span>
                <span className="dv-row-meta">
                  <span>{DEVICE_TYPE_LABEL[d.type] || d.type}</span>
                  {d.sn && <span className="mono">SN {d.sn}</span>}
                  {d.type === "console" && <span className="badge info">待控制代理认证</span>}
                </span>
              </div>
              <span className={"dv-pill " + (st.on ? "on" : "dim")}>{st.text}</span>
              <span className="dv-spacer" />
              <button
                className="btn small primary"
                disabled={!d.online}
                title={d.online ? "选择接管车辆" : "设备须由受信控制代理注册并在线确认后才能接管"}
                onClick={() => setPickerDevice(d)}
              >
                使用
              </button>
              <button className="btn small ghost" onClick={() => void doUnbind(d)}>解绑</button>
            </div>
          );
        })}
      </div>
      {loaded && devices.length === 0 && (
        <div className="dv-foot-hint">
          尚未绑定任何接管设备，点上方「绑定设备」登记设备准入信息；受信控制代理在线注册后才能发起远程接管。
        </div>
      )}
      {devices.length > 0 && (
        <div className="dv-foot-hint">
          已绑定设备仍须由受信控制代理完成在线注册；确认在线后才可选择车辆并发起远程接管。
        </div>
      )}

      {/* 弹窗 1：左右滑动选择设备类型 */}
      {typePickerOpen && (
        <Modal title="选择设备类型" onClose={() => setTypePickerOpen(false)} width="min(760px, 94vw)">
          <div className="dtp-hint">← 左右滑动查看，点击卡片选择设备类型 →</div>
          <div className="dtp-track">
            {TYPES.map((t) => (
              <button
                key={t.id}
                className={"dtp-card t-" + t.letter.toLowerCase()}
                onClick={() => {
                  setTypePickerOpen(false);
                  setBindType(t.id);
                }}
              >
                <span className={"dv-letter " + t.letter.toLowerCase()}>{t.letter}</span>
                <img src={t.img} alt={t.label} />
                <span className="dtp-name">{t.label}</span>
                <span className="dtp-sub">{t.sub}</span>
              </button>
            ))}
          </div>
        </Modal>
      )}

      {/* 弹窗 2：具体绑定输入 */}
      {bindType && (
        <Modal title={bindModalTitle} onClose={() => setBindType(null)} width="min(560px, 94vw)">
          {bindType === "console" && (
            <div className="dv-form">
              <div className="dv-field">
                <span className="dv-field-ic"><HashIcon /></span>
                <input
                  value={consoleSN}
                  onChange={(e) => setConsoleSN(e.target.value)}
                  placeholder="设备 SN 码，如 RS-2026-A0001"
                />
              </div>
              <div className="dv-field">
                <span className="dv-field-ic"><LockIcon /></span>
                <input
                  type="password"
                  value={consolePwd}
                  onChange={(e) => setConsolePwd(e.target.value)}
                  placeholder="设备密码"
                />
              </div>
              <div className="muted" style={{ fontSize: 12, lineHeight: 1.7 }}>
                绑定仅登记设备准入信息；需由部署在受控终端的一体机控制代理完成认证注册，平台才会允许签发接管租约。
              </div>
              <button
                className="dv-bind-btn blue"
                disabled={busy || consoleSN.trim() === "" || consolePwd === ""}
                onClick={() =>
                  void doBind({ type: "console", name: "自研一体机", sn: consoleSN.trim(), password: consolePwd })
                }
              >
                绑定
              </button>
            </div>
          )}
          {bindType === "keyboard" && (
            <div className="dv-form">
              <div className="muted" style={{ fontSize: 12, lineHeight: 1.7 }}>
                键盘绑定只登记输入设备准入信息。浏览器键盘事件不能直接成为车端控制通道，必须由受信控制代理接管并上报在线状态。
              </div>
              <button
                className="dv-bind-btn green"
                disabled={busy}
                onClick={() => void doBind({ type: "keyboard", name: "键盘控制" })}
              >
                <KbIcon /> 绑定键盘控制
              </button>
            </div>
          )}
          {(bindType === "rc" || bindType === "wheel") && (
            <div className="dv-form">
              {pads.filter((p) => (bindType === "wheel" ? p.isWheel : true)).length === 0 ? (
                <div className={"dv-info " + (bindType === "wheel" ? "d" : "b")}>
                  <span className="dv-info-ic">ⓘ</span>
                  <span>
                    未检测到 USB 外设 —— 将{bindType === "wheel" ? "罗技方向盘" : "航模遥控器（USB 教练线/接收器）"}
                    接入本机 USB 后自动识别（如未出现，先点击页面任意处激活浏览器手柄权限）。
                  </span>
                </div>
              ) : (
                <div className="dv-pads">
                  {pads
                    .filter((p) => (bindType === "wheel" ? p.isWheel : true))
                    .map((p) => {
                      const bound = padIsBound(p);
                      return (
                        <div className="dv-bound-row" key={p.index + ":" + p.id}>
                          <span className="dv-dot on" />
                          <span className="dv-bound-name mono" title={p.id}>{p.id}</span>
                          {bindType === "wheel" && p.isWheel && <span className="badge info">方向盘特征</span>}
                          <span className="dv-spacer" />
                          {bound ? (
                            <span className="badge ok">已绑定</span>
                          ) : (
                            <button
                              className="btn small primary"
                              disabled={busy}
                              onClick={() => void doBind({ type: bindType, name: p.id })}
                            >
                              绑定
                            </button>
                          )}
                        </div>
                      );
                    })}
                </div>
              )}
            </div>
          )}
        </Modal>
      )}

      {pickerDevice && (
        <VehiclePickerModal
          fleet={fleet}
          device={pickerDevice}
          onClose={() => setPickerDevice(null)}
          onTaken={(vid) => {
            setPickerDevice(null);
            window.location.hash = "#/driveview/" + encodeURIComponent(vid);
          }}
        />
      )}
    </div>
  );
}

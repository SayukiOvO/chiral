import { useState } from "react";
import { api, type Node } from "../api";
import { Button } from "./ui";
import { Field, Modal, inputCls } from "./primitives";
import { useT } from "../lib/i18n";

/**
 * A node's two names.
 *
 * They have two audiences, which is why there are two. The internal name is
 * what an operator uses to find a box and usually says which provider and
 * datacentre it is; the customer-facing one is all a subscriber ever sees.
 * Leaving the second blank is fine — the portal numbers the line — but it
 * never falls back to the first, so an unset node stays anonymous rather than
 * quietly telling every subscriber where it is hosted.
 */
export function NodeNameDialog({
  node,
  onClose,
  onSaved,
}: {
  node: Node;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t, tf } = useT();
  const [name, setName] = useState(node.name);
  const [displayName, setDisplayName] = useState(node.display_name ?? "");
  const [address, setAddress] = useState(node.address ?? "");
  const [rate, setRate] = useState(String(node.traffic_rate ?? 1));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.updateNode(node.id, {
        name: name.trim(),
        display_name: displayName.trim(),
        address: address.trim(),
        traffic_rate: Number(rate) || 1,
      });
      onSaved();
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose}>
      <form onSubmit={save}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("节点设置")}</h3>
        <p className="mt-1 text-sm text-muted">
          {t("内部名称用于运维标识，对客名称向订阅者展示，连接地址写入订阅。")}
        </p>

        <Field label={t("内部名")}>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="tokyo-1"
            className={inputCls}
          />
          <span className="mt-1 block text-xs text-faint">
            {t("仅在控制台显示，不会发送给订阅者。")}
          </span>
        </Field>

        <Field label={t("对客名称")}>
          <input
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            placeholder={t("例如 日本 · 东京 01")}
            className={inputCls}
          />
          <span className="mt-1 block text-xs text-faint">
            {t("留空时门户按序号显示为「线路 01」，不会回落到内部名。")}
          </span>
        </Field>

        {/* The detected value comes from the peer address of the agent's
            connection, which is whatever the last hop saw — a NAT between
            agent and panel makes it a private address, and a subscription
            built from it points somewhere nobody can reach. */}
        <Field label={t("客户端连接地址")}>
          <input
            value={address}
            onChange={(e) => setAddress(e.target.value)}
            placeholder={node.public_ip || "203.0.113.9"}
            className={inputCls}
          />
          <span className="mt-1 block text-xs text-faint">
            {address.trim()
              ? t("订阅与模板中的 {{node.address}} 用这个值。")
              : tf("留空时使用探测到的 {ip}。该地址取自 Agent 连接的对端地址，若链路中存在 NAT 则并不可靠。", {
                  ip: node.public_ip || t("（尚未探测到）"),
                })}
          </span>
        </Field>

        {/* Applies to the quota, not to the counters: what the agent measured
            stays what it measured, and the quota takes the billed amount. One
            number for both would answer neither question. */}
        <Field label={t("流量倍率")}>
          <input
            type="number"
            min="0.1"
            step="0.1"
            value={rate}
            onChange={(e) => setRate(e.target.value)}
            className={inputCls}
          />
          <span className="mt-1 block text-xs text-faint">
            {t("1 表示按实际用量计入配额。成本较高的线路可调高此值，例如 2 表示每传输 1 GB 计入 2 GB。")}
          </span>
        </Field>

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
            {busy ? t("保存中…") : t("保存")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

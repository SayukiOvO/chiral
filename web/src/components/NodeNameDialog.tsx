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
  const { t } = useT();
  const [name, setName] = useState(node.name);
  const [displayName, setDisplayName] = useState(node.display_name ?? "");
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
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("重命名节点")}</h3>
        <p className="mt-1 text-sm text-muted">
          {t("内部名给运维看，对客名称给订阅者看。")}
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
            {t("只在控制台出现，不会发给订阅者。")}
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
            {t("留空则门户显示「线路 01」这样的编号，不会回落到内部名。")}
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

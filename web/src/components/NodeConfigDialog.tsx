import { useEffect, useState } from "react";
import { api, type ConfigPreview, type Node } from "../api";
import { Button } from "./ui";
import { TemplateEditor } from "./TemplateEditor";
import { useIsDark } from "../lib/theme";
import { Modal } from "./primitives";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

const DEFAULT_SKELETON = `{
  "log": { "loglevel": "warning" },
  "outbounds": [
    { "protocol": "freedom", "tag": "direct" },
    { "protocol": "blackhole", "tag": "block" }
  ]
}`;

/**
 * A node's config: the operator-owned skeleton (everything but inbounds),
 * plus a preview of what assembling the bound profiles produces — validated
 * with `xray -test` before it can be applied.
 */
export function NodeConfigDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const { t, tf } = useT();
  const dark = useIsDark();
  const [skeleton, setSkeleton] = useState(DEFAULT_SKELETON);
  const [preview, setPreview] = useState<ConfigPreview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [applied, setApplied] = useState("");

  async function loadPreview() {
    setError("");
    try {
      setPreview(await api.previewConfig(node.id));
    } catch (e) {
      setPreview(null);
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    loadPreview();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node.id]);

  async function saveSkeleton() {
    setBusy(true);
    setError("");
    try {
      const parsed = JSON.parse(skeleton);
      await api.putSkeleton(node.id, parsed);
      await loadPreview();
    } catch (e) {
      setError(
        e instanceof SyntaxError
          ? tf("骨架不是合法 JSON：{msg}", { msg: e.message })
          : (e as Error).message,
      );
    } finally {
      setBusy(false);
    }
  }

  async function apply() {
    setBusy(true);
    setError("");
    try {
      const r = await api.applyConfig(node.id);
      setApplied(tf("已下发，版本 v{n}", { n: r.version }));
      setTimeout(() => setApplied(""), 2500);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose} wide>
      <div className="flex items-start justify-between gap-4">
        <div>
          <h3 className="font-display text-lg font-semibold tracking-tight">
            {node.name} · {t("配置")}
          </h3>
          <p className="mt-1 text-sm text-muted">
            {t("骨架是 inbounds 之外的部分；inbounds 由绑定的接入配置渲染装配。")}
          </p>
        </div>
        <Button variant="ghost" onClick={onClose}>
          {t("关闭")}
        </Button>
      </div>

      {error && (
        <div
          className="mt-4 rounded-xl px-4 py-3 text-sm text-danger"
          style={{
            border: "1px solid color-mix(in srgb, var(--danger) 35%, transparent)",
            background: "color-mix(in srgb, var(--danger) 10%, transparent)",
          }}
        >
          {error}
        </div>
      )}

      <div className="mt-5">
        <div className="mb-2 flex items-center justify-between">
          <h4 className="text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
            {t("config 骨架")}
          </h4>
          <Button size="sm" variant="outline" onClick={saveSkeleton} disabled={busy}>
            {t("保存骨架")}
          </Button>
        </div>
        {/* The skeleton is plain JSON, but the same editor keeps the look
            consistent and highlights any {{variable}} left in by mistake. */}
        <TemplateEditor
          value={skeleton}
          onChange={setSkeleton}
          known={new Set()}
          dark={dark}
          height={180}
        />
      </div>

      <div className="mt-6">
        <div className="mb-2 flex items-center justify-between gap-3">
          <h4 className="text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
            {t("装配预览")}
          </h4>
          <div className="flex items-center gap-2">
            {preview && <TestBadge tested={preview.tested} error={preview.test_error} />}
            <Button
              size="sm"
              variant="primary"
              onClick={apply}
              disabled={busy || !preview || (!!preview.test_error && preview.tested)}
            >
              {applied || t("下发")}
            </Button>
          </div>
        </div>

        {preview ? (
          <>
            {preview.inbound_tags.length > 0 && (
              <div className="mb-2 flex flex-wrap gap-1.5">
                {preview.inbound_tags.map((t) => (
                  <span
                    key={t}
                    className="rounded-md px-1.5 py-0.5 font-mono text-[11px] text-muted"
                    style={{ background: "color-mix(in srgb, var(--muted) 10%, transparent)" }}
                  >
                    {t}
                  </span>
                ))}
              </div>
            )}
            <pre className="scroll-slim max-h-72 overflow-auto rounded-xl border border-line bg-paper p-4 font-mono text-[11.5px] leading-relaxed">
              {JSON.stringify(preview.config, null, 2)}
            </pre>
          </>
        ) : (
          <p className="rounded-xl border border-dashed border-line-strong px-4 py-8 text-center text-sm text-muted">
            {t("无法装配。先绑定接入配置，并确认模板里的变量都已定义。")}
          </p>
        )}
      </div>
    </Modal>
  );
}

function TestBadge({ tested, error }: { tested: boolean; error: string }) {
  const { t } = useT();
  if (!tested) {
    return (
      <span className="text-xs text-muted" title={t("面板未配置 xray 二进制，下发前不做预校验")}>
        {t("未校验")}
      </span>
    );
  }
  return (
    <span
      className={cn("text-xs", error ? "text-danger" : "text-online")}
      title={error || t("xray -test 通过")}
    >
      {error ? t("xray -test 未通过") : t("xray -test 通过")}
    </span>
  );
}

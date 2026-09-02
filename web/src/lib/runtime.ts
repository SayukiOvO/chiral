import type { Node } from "../api";

export type RuntimeHealthLabel =
  | "适配就绪"
  | "适配不兼容"
  | "契约就绪"
  | "契约不兼容"
  | "不可达"
  | "状态未知"
  | "观测已过期";

export interface RuntimeHealthPresentation {
  label: RuntimeHealthLabel;
  tone: "ready" | "danger" | "unknown";
}

// The Agent re-reads the live OpenAPI contract every five minutes. One minute
// of grace avoids a healthy card flickering stale around that refresh.
const STALE_AFTER_SECONDS = 6 * 60;

export function runtimeHealthPresentation(
  node: Pick<Node, "runtime_health" | "runtime_mode" | "runtime_observed_at">,
  nowUnix = Date.now() / 1000,
): RuntimeHealthPresentation {
  if (node.runtime_health !== "UNKNOWN" && !node.runtime_observed_at) {
    return { label: "状态未知", tone: "unknown" };
  }
  if (
    node.runtime_health !== "UNKNOWN" &&
    node.runtime_observed_at &&
    nowUnix - node.runtime_observed_at > STALE_AFTER_SECONDS
  ) {
    return { label: "观测已过期", tone: "danger" };
  }
  switch (node.runtime_health) {
    case "READY":
      if (node.runtime_mode === "SHADOW") {
        return { label: "契约就绪", tone: "ready" };
      }
      if (node.runtime_mode === "ACTIVE") {
        return { label: "适配就绪", tone: "ready" };
      }
      return { label: "状态未知", tone: "unknown" };
    case "INCOMPATIBLE":
      return {
        label: node.runtime_mode === "SHADOW" ? "契约不兼容" : "适配不兼容",
        tone: "danger",
      };
    case "UNREACHABLE":
      return { label: "不可达", tone: "danger" };
    default:
      return { label: "状态未知", tone: "unknown" };
  }
}

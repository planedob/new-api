import type {
  CatalogModel,
  FakeResponse,
  FakeScenario,
  ImageOperation,
  LocalRequestEvent,
} from "./types";

export const LOCAL_IMAGE_FIXTURE = "local-fixed-256x256.png";
const imageSvg = `<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512"><defs><linearGradient id="g" x1="0" x2="1" y1="0" y2="1"><stop stop-color="#0f766e"/><stop offset="1" stop-color="#164e63"/></linearGradient></defs><rect width="512" height="512" rx="36" fill="url(#g)"/><circle cx="150" cy="160" r="70" fill="#fef3c7" opacity=".9"/><path d="M70 418 205 250l82 92 54-58 104 134Z" fill="#99f6e4" opacity=".85"/><text x="256" y="468" text-anchor="middle" fill="white" font-family="sans-serif" font-size="26">LOCAL / ISOLATED</text></svg>`;
const videoSvg = `<svg xmlns="http://www.w3.org/2000/svg" width="960" height="540"><rect width="960" height="540" fill="#111827"/><rect x="60" y="60" width="840" height="420" rx="28" fill="#1e3a8a"/><circle cx="480" cy="270" r="72" fill="#f59e0b"/><path d="m460 228 82 42-82 42Z" fill="#111827"/><text x="480" y="430" text-anchor="middle" fill="white" font-family="sans-serif" font-size="30">LOCAL H3 VIDEO FIXTURE</text></svg>`;
export const LOCAL_IMAGE_DATA_URL = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(imageSvg)}`;
export const LOCAL_VIDEO_DATA_URL = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(videoSvg)}`;

export const LOCAL_CATALOG: CatalogModel[] = [
  {
    model: "gpt-4o-mini",
    vendor: "OpenAI",
    group: "通用模型",
    operations: ["chat", "responses"],
    parameter_bounds: { stream: true, max_tokens: 16384 },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "local_fixture",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按 token（脱敏快照）",
    performance: { latency_ms: 420, success_rate: 0.99, throughput: "high" },
    tags: ["文本", "流式"],
    endpoint_type: "chat",
  },
  {
    model: "gpt-image-2",
    vendor: "OpenAI",
    group: "生图模型",
    operations: ["image_generation", "image_edits"],
    parameter_bounds: {
      size: ["1024x1024", "1536x1024", "1024x1536"],
      quality: ["auto", "medium", "high"],
      n: "1-4",
    },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "local_fixture",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按请求（脱敏快照）",
    performance: { latency_ms: 1800, success_rate: 0.97, throughput: "medium" },
    tags: ["图片", "generation", "edits"],
    endpoint_type: "images",
  },
  {
    model: "gemini-3-pro-image-preview",
    vendor: "Google",
    group: "生图模型",
    operations: ["image_generation", "image_edits"],
    parameter_bounds: { resolution: ["1K", "2K"], quality: ["auto"] },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "local_fixture",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按请求（脱敏快照）",
    performance: { latency_ms: 2400, success_rate: 0.95, throughput: "medium" },
    tags: ["图片", "原生签名"],
    endpoint_type: "images",
  },
  {
    model: "gemini-3.1-flash-image-preview",
    vendor: "Google",
    group: "生图模型",
    operations: ["image_generation", "image_edits"],
    parameter_bounds: { resolution: ["1K", "2K", "4K"], quality: ["auto"] },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "local_fixture",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按请求（脱敏快照）",
    performance: { latency_ms: 1300, success_rate: 0.98, throughput: "high" },
    tags: ["图片", "快速"],
    endpoint_type: "images",
  },
  {
    model: "minimax-h3",
    vendor: "MiniMax",
    group: "视频模型",
    operations: ["video_generation"],
    parameter_bounds: {
      duration_seconds: [5, 6, 10, 15],
      resolution: ["1K", "2K"],
    },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "local_fixture",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按请求（脱敏快照）",
    performance: { latency_ms: 5200, success_rate: 0.94, throughput: "queue" },
    tags: ["视频", "异步"],
    endpoint_type: "video",
  },
  {
    model: "glm-5.2",
    vendor: "智谱",
    group: "国产模型",
    operations: ["chat", "responses"],
    parameter_bounds: { stream: true, max_tokens: 32768 },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "unknown",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按 token（脱敏快照）",
    performance: {
      latency_ms: null,
      success_rate: null,
      throughput: "unknown",
    },
    tags: ["文本", "国产"],
    endpoint_type: "chat",
  },
  {
    model: "kimi-k3",
    vendor: "月之暗面",
    group: "国产模型",
    operations: ["chat"],
    parameter_bounds: { stream: true, max_tokens: 32768 },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "unknown",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按 token（脱敏快照）",
    performance: {
      latency_ms: null,
      success_rate: null,
      throughput: "unknown",
    },
    tags: ["文本", "国产"],
    endpoint_type: "chat",
  },
  {
    model: "deepseek-v4-pro",
    vendor: "DeepSeek",
    group: "国产模型",
    operations: ["chat", "responses"],
    parameter_bounds: { stream: true, max_tokens: 32768 },
    cataloged: true,
    selectable: true,
    testable: true,
    verified: "unknown",
    verification_scope: "LOCAL_FIXTURE",
    price_summary: "按 token（脱敏快照）",
    performance: {
      latency_ms: null,
      success_rate: null,
      throughput: "unknown",
    },
    tags: ["文本", "国产"],
    endpoint_type: "chat",
  },
];

export const LOCAL_SCENARIOS: Array<{ value: FakeScenario; label: string }> = [
  { value: "success", label: "成功" },
  { value: "bad_request", label: "400 参数错误" },
  { value: "rate_limit", label: "429 限流" },
  { value: "server_error", label: "5xx 上游错误" },
  { value: "timeout", label: "超时" },
  { value: "empty_result", label: "空结果" },
  { value: "cancelled", label: "取消" },
  { value: "safety_rejected", label: "内容安全拒绝" },
  { value: "accepted_disconnect", label: "已接受后断连" },
];

export function endpointForOperation(operation: string): {
  endpoint: string;
  method: "POST" | "GET";
  request_shape: "json" | "multipart" | "poll";
} {
  if (operation === "image_generation")
    return {
      endpoint: "/pg/images/generations",
      method: "POST",
      request_shape: "json",
    };
  if (operation === "image_edits")
    return {
      endpoint: "/pg/images/edits",
      method: "POST",
      request_shape: "multipart",
    };
  if (operation === "video_generation")
    return {
      endpoint: "/pg/images/jobs",
      method: "POST",
      request_shape: "json",
    };
  if (operation === "video_poll")
    return {
      endpoint: "/pg/images/jobs/{task_id}",
      method: "GET",
      request_shape: "poll",
    };
  return {
    endpoint: "/pg/chat/completions",
    method: "POST",
    request_shape: "json",
  };
}

function localId(prefix: string): string {
  const suffix =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID().slice(0, 12)
      : `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`;
  return `${prefix}-${suffix}`;
}
function wait(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      window.clearTimeout(timer);
      signal?.removeEventListener("abort", onAbort);
      reject(new DOMException("local request aborted", "AbortError"));
    };
    if (signal?.aborted) onAbort();
    else signal?.addEventListener("abort", onAbort, { once: true });
  });
}

export class FakeUpstreamError extends Error {
  readonly status: number | null;
  readonly stage: string;
  readonly event: LocalRequestEvent;
  constructor(
    message: string,
    event: LocalRequestEvent,
    status: number | null,
    stage: string,
  ) {
    super(message);
    this.name = "FakeUpstreamError";
    this.status = status;
    this.stage = stage;
    this.event = event;
  }
}

export async function runLocalFakeRequest(input: {
  operation: string;
  model: string;
  scenario: FakeScenario;
  signal?: AbortSignal;
  onEvent?: (event: LocalRequestEvent) => void;
}): Promise<FakeResponse> {
  const started = Date.now();
  const contract = endpointForOperation(input.operation);
  const event: LocalRequestEvent = {
    id: localId("local"),
    operation: input.operation,
    endpoint: contract.endpoint,
    method: contract.method,
    model: input.model,
    scenario: input.scenario,
    request_shape: contract.request_shape,
    upstream_called: input.scenario !== "cancelled",
    retried: false,
    simulated_billing: false,
    status: null,
    stage:
      input.scenario === "cancelled" ? "cancelled_before_send" : "request_sent",
    final_status: input.scenario === "cancelled" ? "cancelled" : "running",
    duration_ms: 0,
    created_at: new Date().toISOString(),
  };
  input.onEvent?.(event);
  try {
    await wait(input.scenario === "timeout" ? 950 : 380, input.signal);
  } catch (error) {
    if (!(error instanceof DOMException) || error.name !== "AbortError")
      throw error;
    const cancelledEvent: LocalRequestEvent = {
      ...event,
      upstream_called: false,
      status: null,
      stage: "cancelled",
      final_status: "cancelled",
      simulated_billing: false,
      duration_ms: Date.now() - started,
    };
    input.onEvent?.(cancelledEvent);
    throw new FakeUpstreamError(
      "local request cancelled",
      cancelledEvent,
      null,
      "cancelled",
    );
  }
  const responseStatus: Record<FakeScenario, number | null> = {
    success: 200,
    bad_request: 400,
    rate_limit: 429,
    server_error: 502,
    timeout: null,
    empty_result: 200,
    cancelled: null,
    safety_rejected: 400,
    accepted_disconnect: 502,
  };
  const failed = input.scenario !== "success";
  const finalEvent: LocalRequestEvent = {
    ...event,
    status: responseStatus[input.scenario],
    stage:
      input.scenario === "timeout"
        ? "transport_timeout"
        : input.scenario === "accepted_disconnect"
          ? "upstream_accepted_connection_lost"
          : input.scenario === "empty_result"
            ? "response_empty"
            : input.scenario === "cancelled"
              ? "cancelled"
              : failed
                ? "upstream_rejected"
                : "response_written",
    final_status:
      input.scenario === "cancelled"
        ? "cancelled"
        : failed
          ? "failed"
          : "completed",
    simulated_billing: input.scenario === "success",
    duration_ms: Date.now() - started,
  };
  input.onEvent?.(finalEvent);
  if (failed)
    throw new FakeUpstreamError(
      input.scenario === "empty_result"
        ? "fake-upstream returned an empty result"
        : `fake-upstream ${input.scenario}`,
      finalEvent,
      finalEvent.status,
      finalEvent.stage,
    );
  if (
    input.operation === "image_generation" ||
    input.operation === "image_edits"
  )
    return { request: finalEvent, image_url: LOCAL_IMAGE_DATA_URL };
  if (input.operation === "video_generation")
    return { request: finalEvent, task_id: localId("task") };
  return {
    request: finalEvent,
    text: "这是 LOCAL/ISOLATED fake-upstream 返回的模拟结果。未访问真实上游，未产生真实账务。",
  };
}

export function buildLocalVideoPoll(
  task: LocalRequestEvent,
  pollNumber: number,
): LocalRequestEvent {
  return {
    ...task,
    id: `${task.id}-poll-${pollNumber}`,
    operation: "video_poll",
    endpoint: "/pg/images/jobs/{task_id}",
    method: "GET",
    request_shape: "poll",
    stage: pollNumber >= 2 ? "response_written" : "polling",
    final_status: pollNumber >= 2 ? "completed" : "running",
    status: 200,
    duration_ms: pollNumber >= 2 ? 240 : 100,
    simulated_billing: pollNumber >= 2,
  };
}
export function imageOperationLabel(operation: ImageOperation): string {
  return operation === "image_generation"
    ? "文生图 generation"
    : "图生图 edits";
}

import { useMemo, useRef, useState } from "react";
import {
  CheckCircle2,
  ChevronRight,
  CircleStop,
  Clock3,
  Filter,
  Image as ImageIcon,
  LayoutGrid,
  List,
  MessageSquare,
  Play,
  Search,
  Sparkles,
  Square,
  Video,
  XCircle,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  LOCAL_CATALOG,
  LOCAL_IMAGE_FIXTURE,
  LOCAL_SCENARIOS,
  LOCAL_VIDEO_DATA_URL,
  buildLocalVideoPoll,
  FakeUpstreamError,
  imageOperationLabel,
  runLocalFakeRequest,
} from "./fixture";
import type {
  CatalogModel,
  CatalogView,
  FakeScenario,
  ImageOperation,
  LocalRequestEvent,
  WorkbenchSurface,
  WorkbenchTab,
  WorkbenchTask,
} from "./types";

const tabs: Array<{
  value: WorkbenchTab;
  label: string;
  icon: typeof MessageSquare;
}> = [
  { value: "chat", label: "聊天", icon: MessageSquare },
  { value: "image", label: "图片", icon: ImageIcon },
  { value: "video", label: "视频", icon: Video },
  { value: "tasks", label: "任务", icon: Clock3 },
];
function metric(value: number | null, suffix = ""): string {
  return value == null ? "unknown" : `${value}${suffix}`;
}
function statusIcon(status: WorkbenchTask["final_status"]) {
  if (status === "completed")
    return <CheckCircle2 className="size-4 text-emerald-500" />;
  if (status === "failed")
    return <XCircle className="text-destructive size-4" />;
  if (status === "cancelled")
    return <CircleStop className="size-4 text-amber-500" />;
  return <Clock3 className="size-4 text-blue-500" />;
}

export function ModelWorkbench() {
  const localEnabled =
    import.meta.env.VITE_MODEL_WORKBENCH_LOCAL_FIXTURE === "true" &&
    (window.location.hostname === "127.0.0.1" ||
      window.location.hostname === "localhost" ||
      window.location.hostname === "[::1]");
  const [surface, setSurface] = useState<WorkbenchSurface>("catalog");
  const [tab, setTab] = useState<WorkbenchTab>("chat");
  const [catalogView, setCatalogView] = useState<CatalogView>("cards");
  const [search, setSearch] = useState("");
  const [groupFilter, setGroupFilter] = useState("全部分组");
  const [vendorFilter, setVendorFilter] = useState("全部供应商");
  const [operationFilter, setOperationFilter] = useState("全部能力");
  const [selected, setSelected] = useState<CatalogModel | null>(
    LOCAL_CATALOG[1] ?? null,
  );
  const [selectedModel, setSelectedModel] = useState("gpt-image-2");
  const [scenario, setScenario] = useState<FakeScenario>("success");
  const [tasks, setTasks] = useState<WorkbenchTask[]>([]);
  const [events, setEvents] = useState<LocalRequestEvent[]>([]);
  const [activeTaskId, setActiveTaskId] = useState<string | null>(null);
  const [result, setResult] = useState<{
    kind: "text" | "image" | "video" | "error";
    value: string;
    meta?: LocalRequestEvent;
  } | null>(null);
  const cancelledRuns = useRef(new Set<string>());
  const activeControllers = useRef(new Map<string, AbortController>());
  const [chatPrompt, setChatPrompt] =
    useState("用一句话介绍这个本地模型广场。");
  const [chatStream, setChatStream] = useState(true);
  const [imageOperation, setImageOperation] =
    useState<ImageOperation>("image_generation");
  const [imagePrompt, setImagePrompt] =
    useState("一座海边城市的清晨，电影感光线");
  const [imageRatio, setImageRatio] = useState("1:1");
  const [imageResolution, setImageResolution] = useState("1024x1024");
  const [imageQuality, setImageQuality] = useState("auto");
  const [imageQuantity, setImageQuantity] = useState("1");
  const [videoPrompt, setVideoPrompt] = useState("一段平稳推进的城市夜景镜头");
  const [videoRatio, setVideoRatio] = useState("16:9");
  const [videoResolution, setVideoResolution] = useState("2K");
  const [videoDuration, setVideoDuration] = useState("5");

  const groups = useMemo(
    () => ["全部分组", ...new Set(LOCAL_CATALOG.map((item) => item.group))],
    [],
  );
  const vendors = useMemo(
    () => ["全部供应商", ...new Set(LOCAL_CATALOG.map((item) => item.vendor))],
    [],
  );
  const filteredCatalog = useMemo(
    () =>
      LOCAL_CATALOG.filter((item) => {
        const needle = search.trim().toLowerCase();
        const text =
          `${item.model} ${item.vendor} ${item.group} ${item.tags.join(" ")}`.toLowerCase();
        return (
          (!needle || text.includes(needle)) &&
          (groupFilter === "全部分组" || item.group === groupFilter) &&
          (vendorFilter === "全部供应商" || item.vendor === vendorFilter) &&
          (operationFilter === "全部能力" ||
            item.operations.includes(operationFilter))
        );
      }),
    [groupFilter, operationFilter, search, vendorFilter],
  );
  const activeWorkbenchModel =
    tab === "video"
      ? "minimax-h3"
      : LOCAL_CATALOG.some(
            (item) =>
              item.model === selectedModel &&
              item.operations.includes(tab === "image" ? imageOperation : tab),
          )
        ? selectedModel
        : tab === "image"
          ? "gpt-image-2"
          : "gpt-4o-mini";

  const selectModel = (item: CatalogModel) => {
    setSelected(item);
    setSelectedModel(item.model);
    setSurface("workbench");
    if (item.operations.includes("image_generation")) setTab("image");
    else if (item.operations.includes("video_generation")) setTab("video");
    else setTab("chat");
  };
  const addEvent = (event: LocalRequestEvent) =>
    setEvents((current) =>
      [event, ...current.filter((entry) => entry.id !== event.id)].slice(0, 18),
    );
  const updateTask = (taskId: string, patch: Partial<WorkbenchTask>) =>
    setTasks((current) =>
      current.map((task) =>
        task.id === taskId ? { ...task, ...patch } : task,
      ),
    );
  const execute = async (operation: string, model: string, title: string) => {
    const runId = `run-${Date.now()}`;
    const controller = new AbortController();
    activeControllers.current.set(runId, controller);
    setActiveTaskId(runId);
    setResult(null);
    const placeholder: WorkbenchTask = {
      id: runId,
      operation,
      endpoint:
        operation === "image_generation"
          ? "/pg/images/generations"
          : operation === "image_edits"
            ? "/pg/images/edits"
            : operation === "video_generation"
              ? "/pg/images/jobs"
              : "/pg/chat/completions",
      method: "POST",
      model,
      scenario,
      request_shape: operation === "image_edits" ? "multipart" : "json",
      upstream_called: scenario !== "cancelled",
      retried: false,
      simulated_billing: false,
      status: null,
      stage:
        scenario === "cancelled" ? "cancelled_before_send" : "request_sent",
      final_status: scenario === "cancelled" ? "cancelled" : "running",
      duration_ms: 0,
      created_at: new Date().toISOString(),
      title,
    };
    setTasks((current) => [placeholder, ...current].slice(0, 20));
    try {
      const response = await runLocalFakeRequest({
        operation,
        model,
        scenario,
        signal: controller.signal,
        onEvent: addEvent,
      });
      if (cancelledRuns.current.has(runId)) return;
      updateTask(runId, response.request);
      addEvent(response.request);
      if (response.image_url)
        setResult({
          kind: "image",
          value: response.image_url,
          meta: response.request,
        });
      else if (response.task_id) {
        setResult({
          kind: "video",
          value: LOCAL_VIDEO_DATA_URL,
          meta: response.request,
        });
        window.setTimeout(() => {
          const poll = buildLocalVideoPoll(response.request, 1);
          addEvent(poll);
          updateTask(runId, { stage: "polling", final_status: "running" });
          window.setTimeout(() => {
            const done = buildLocalVideoPoll(response.request, 2);
            addEvent(done);
            updateTask(runId, { ...done, title });
            setResult({
              kind: "video",
              value: LOCAL_VIDEO_DATA_URL,
              meta: done,
            });
          }, 420);
        }, 250);
      } else
        setResult({
          kind: "text",
          value: response.text ?? "",
          meta: response.request,
        });
    } catch (error) {
      if (cancelledRuns.current.has(runId)) return;
      const failure = error instanceof FakeUpstreamError ? error : null;
      if (failure) {
        addEvent(failure.event);
        updateTask(runId, { ...failure.event, title });
        setResult({
          kind: "error",
          value: `${failure.message}（HTTP ${failure.status ?? "timeout"}，阶段：${failure.stage}）`,
          meta: failure.event,
        });
      } else {
        updateTask(runId, {
          final_status: "failed",
          stage: "local_fixture_error",
          title,
        });
        setResult({
          kind: "error",
          value: "本地 fixture 执行失败",
          meta: placeholder,
        });
      }
    } finally {
      activeControllers.current.delete(runId);
      setActiveTaskId(null);
    }
  };
  const stopActive = () => {
    if (!activeTaskId) return;
    cancelledRuns.current.add(activeTaskId);
    activeControllers.current.get(activeTaskId)?.abort();
    updateTask(activeTaskId, {
      final_status: "cancelled",
      stage: "cancelled_by_user",
      upstream_called: false,
      simulated_billing: false,
    });
    setResult({
      kind: "error",
      value: "已停止：没有重试、没有降级、没有模拟扣费。",
    });
    setActiveTaskId(null);
  };

  if (!localEnabled)
    return (
      <main className="bg-background text-foreground flex min-h-screen items-center justify-center p-6">
        <div className="max-w-lg rounded-2xl border p-8 text-center">
          <Badge variant="outline">LOCAL/ISOLATED</Badge>
          <h1 className="mt-4 text-2xl font-semibold">本地工作台未启用</h1>
          <p className="text-muted-foreground mt-2">
            请使用 `VITE_MODEL_WORKBENCH_LOCAL_FIXTURE=true`
            启动。开关关闭时，能力目录和 fake-upstream 默认不可用。
          </p>
        </div>
      </main>
    );

  return (
    <main className="bg-muted/20 min-h-screen p-4 md:p-8">
      <div className="mx-auto max-w-[1500px] space-y-4">
        <header className="bg-background flex flex-wrap items-center justify-between gap-3 rounded-2xl border px-5 py-4 shadow-sm">
          <div>
            <div className="flex items-center gap-2">
              <Sparkles className="text-primary size-5" />
              <h1 className="text-xl font-semibold">Aibuff 模型广场</h1>
              <Badge variant="secondary">LOCAL/ISOLATED</Badge>
            </div>
            <p className="text-muted-foreground mt-1 text-xs">
              固定脱敏目录 · fake-upstream · 零真实网络 · 零真实账务
            </p>
          </div>
          <div className="flex gap-2">
            <Button
              variant={surface === "catalog" ? "default" : "outline"}
              onClick={() => setSurface("catalog")}
            >
              <LayoutGrid className="size-4" />
              模型广场
            </Button>
            <Button
              variant={surface === "workbench" ? "default" : "outline"}
              onClick={() => {
                setSurface("workbench");
                if (
                  tab === "chat" &&
                  !LOCAL_CATALOG.some(
                    (item) =>
                      item.model === selectedModel &&
                      item.operations.includes("chat"),
                  )
                )
                  setSelectedModel("gpt-4o-mini");
              }}
            >
              <Play className="size-4" />
              模拟工作台
            </Button>
          </div>
        </header>
        {surface === "catalog" ? (
          <section className="space-y-4">
            <div className="bg-background rounded-2xl border p-4 shadow-sm">
              <div className="flex flex-wrap items-center gap-2">
                <div className="relative min-w-[240px] flex-1">
                  <Search className="text-muted-foreground absolute top-2.5 left-3 size-4" />
                  <input
                    aria-label="搜索模型"
                    className="bg-muted/30 h-9 w-full rounded-lg border pl-9 pr-3 text-sm outline-none focus:ring-2 focus:ring-primary/30"
                    placeholder="搜索模型、供应商或标签"
                    value={search}
                    onChange={(event) => setSearch(event.target.value)}
                  />
                </div>
                <select
                  aria-label="分组筛选"
                  className="bg-background h-9 rounded-lg border px-3 text-sm"
                  value={groupFilter}
                  onChange={(event) => setGroupFilter(event.target.value)}
                >
                  {groups.map((group) => (
                    <option key={group}>{group}</option>
                  ))}
                </select>
                <select
                  aria-label="供应商筛选"
                  className="bg-background h-9 rounded-lg border px-3 text-sm"
                  value={vendorFilter}
                  onChange={(event) => setVendorFilter(event.target.value)}
                >
                  {vendors.map((vendor) => (
                    <option key={vendor}>{vendor}</option>
                  ))}
                </select>
                <select
                  aria-label="能力筛选"
                  className="bg-background h-9 rounded-lg border px-3 text-sm"
                  value={operationFilter}
                  onChange={(event) => setOperationFilter(event.target.value)}
                >
                  <option>全部能力</option>
                  <option value="chat">chat</option>
                  <option value="responses">responses</option>
                  <option value="image_generation">image_generation</option>
                  <option value="image_edits">image_edits</option>
                  <option value="video_generation">video_generation</option>
                </select>
                <div className="ml-auto flex gap-1 rounded-lg border p-1">
                  <Button
                    size="icon-sm"
                    variant={catalogView === "cards" ? "secondary" : "ghost"}
                    aria-label="卡片视图"
                    onClick={() => setCatalogView("cards")}
                  >
                    <LayoutGrid className="size-4" />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant={catalogView === "table" ? "secondary" : "ghost"}
                    aria-label="表格视图"
                    onClick={() => setCatalogView("table")}
                  >
                    <List className="size-4" />
                  </Button>
                </div>
              </div>
              <div className="text-muted-foreground mt-3 flex items-center gap-2 text-xs">
                <Filter className="size-3.5" />共 {filteredCatalog.length}{" "}
                个脱敏模型 · 状态均限定在 LOCAL_FIXTURE
              </div>
            </div>
            {catalogView === "cards" ? (
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                {filteredCatalog.map((item) => (
                  <article
                    key={item.model}
                    className={cn(
                      "bg-background rounded-2xl border p-5 shadow-sm transition hover:-translate-y-0.5 hover:shadow-md",
                      selected?.model === item.model && "ring-primary ring-2",
                    )}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <Badge variant="outline">{item.vendor}</Badge>
                        <h2 className="mt-3 font-mono text-base font-semibold">
                          {item.model}
                        </h2>
                        <p className="text-muted-foreground mt-1 text-sm">
                          {item.group} · {item.endpoint_type}
                        </p>
                      </div>
                      <Badge
                        variant={
                          item.verified === "unknown" ? "outline" : "secondary"
                        }
                      >
                        {item.verified === "unknown" ? "unknown" : "已验证"}
                      </Badge>
                    </div>
                    <div className="mt-4 grid grid-cols-3 gap-2 text-xs">
                      <div className="bg-muted/40 rounded-lg p-2">
                        <div className="text-muted-foreground">延迟</div>
                        <div className="mt-1 font-medium">
                          {metric(item.performance.latency_ms, "ms")}
                        </div>
                      </div>
                      <div className="bg-muted/40 rounded-lg p-2">
                        <div className="text-muted-foreground">成功率</div>
                        <div className="mt-1 font-medium">
                          {item.performance.success_rate == null
                            ? "unknown"
                            : `${Math.round(item.performance.success_rate * 100)}%`}
                        </div>
                      </div>
                      <div className="bg-muted/40 rounded-lg p-2">
                        <div className="text-muted-foreground">吞吐</div>
                        <div className="mt-1 font-medium">
                          {item.performance.throughput}
                        </div>
                      </div>
                    </div>
                    <div className="mt-4 flex flex-wrap gap-1">
                      {item.tags.map((tag) => (
                        <Badge key={tag} variant="secondary">
                          {tag}
                        </Badge>
                      ))}
                    </div>
                    <div className="mt-5 flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => setSelected(item)}
                      >
                        详情
                        <ChevronRight className="size-3.5" />
                      </Button>
                      <Button size="sm" onClick={() => selectModel(item)}>
                        本地体验
                      </Button>
                    </div>
                  </article>
                ))}
              </div>
            ) : (
              <div className="bg-background overflow-auto rounded-2xl border shadow-sm">
                <table className="w-full min-w-[850px] text-left text-sm">
                  <thead className="bg-muted/40 text-muted-foreground text-xs">
                    <tr>
                      <th className="p-4">模型</th>
                      <th>供应商</th>
                      <th>分组</th>
                      <th>能力</th>
                      <th>性能</th>
                      <th>状态</th>
                      <th className="p-4">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {filteredCatalog.map((item) => (
                      <tr key={item.model} className="border-t">
                        <td className="p-4 font-mono">{item.model}</td>
                        <td>{item.vendor}</td>
                        <td>{item.group}</td>
                        <td>{item.operations.join(" · ")}</td>
                        <td>
                          {metric(item.performance.latency_ms, "ms")} /{" "}
                          {item.performance.throughput}
                        </td>
                        <td>{item.verified}</td>
                        <td className="p-4">
                          <Button size="sm" onClick={() => selectModel(item)}>
                            本地体验
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {selected && (
              <div className="bg-background rounded-2xl border p-5 shadow-sm">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <div className="flex items-center gap-2">
                      <Badge variant="outline">{selected.vendor}</Badge>
                      <Badge variant="secondary">LOCAL_FIXTURE</Badge>
                    </div>
                    <h2 className="mt-2 text-xl font-semibold">
                      {selected.model}
                    </h2>
                    <p className="text-muted-foreground mt-1 text-sm">
                      {selected.group} · {selected.price_summary}
                    </p>
                  </div>
                  <Button onClick={() => selectModel(selected)}>
                    进入本地工作台
                    <ChevronRight className="size-4" />
                  </Button>
                </div>
                <div className="mt-5 grid gap-4 md:grid-cols-3">
                  <div>
                    <h3 className="text-sm font-medium">概览</h3>
                    <p className="text-muted-foreground mt-2 text-xs">
                      可选：{String(selected.selectable)} · 可测：
                      {String(selected.testable)} · 目录：
                      {String(selected.cataloged)}
                    </p>
                    <p className="text-muted-foreground mt-1 text-xs">
                      能力：{selected.operations.join("、")}
                    </p>
                  </div>
                  <div>
                    <h3 className="text-sm font-medium">性能</h3>
                    <p className="text-muted-foreground mt-2 text-xs">
                      延迟：{metric(selected.performance.latency_ms, "ms")} ·
                      成功率：
                      {selected.performance.success_rate == null
                        ? "unknown"
                        : `${Math.round(selected.performance.success_rate * 100)}%`}
                    </p>
                    <p className="text-muted-foreground mt-1 text-xs">
                      吞吐：{selected.performance.throughput}
                    </p>
                  </div>
                  <div>
                    <h3 className="text-sm font-medium">API</h3>
                    <p className="text-muted-foreground mt-2 font-mono text-xs">
                      {selected.operations.includes("image_edits")
                        ? "/pg/images/edits (multipart)"
                        : selected.operations.includes("image_generation")
                          ? "/pg/images/generations (JSON)"
                          : selected.operations.includes("video_generation")
                            ? "/pg/images/jobs (POST + GET)"
                            : "/pg/chat/completions (JSON)"}
                    </p>
                    <p className="text-muted-foreground mt-1 text-xs">
                      无渠道、Base URL、Token 或客户数据
                    </p>
                  </div>
                </div>
              </div>
            )}
          </section>
        ) : (
          <section className="space-y-4">
            <div className="bg-background flex flex-wrap gap-2 rounded-2xl border p-2 shadow-sm">
              {tabs.map(({ value, label, icon: Icon }) => (
                <button
                  key={value}
                  className={cn(
                    "flex h-9 items-center gap-2 rounded-lg px-4 text-sm transition",
                    tab === value
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:bg-muted",
                  )}
                  onClick={() => setTab(value)}
                >
                  <Icon className="size-4" />
                  {label}
                  {value === "tasks" && (
                    <span className="rounded-full bg-black/10 px-1.5 text-xs dark:bg-white/10">
                      {tasks.length}
                    </span>
                  )}
                </button>
              ))}
            </div>
            <div className="grid gap-4 xl:grid-cols-[280px_minmax(0,1fr)_310px]">
              <aside className="bg-background rounded-2xl border p-4 shadow-sm">
                <div className="flex items-center justify-between">
                  <h2 className="font-semibold">参数</h2>
                  <Badge variant="outline">本地</Badge>
                </div>
                {tab === "chat" && (
                  <div className="mt-4 space-y-3">
                    <label className="block text-xs font-medium">
                      模型
                      <select
                        className="bg-background mt-1 h-9 w-full rounded-lg border px-2 text-sm"
                        value={activeWorkbenchModel}
                        onChange={(event) =>
                          setSelectedModel(event.target.value)
                        }
                      >
                        {LOCAL_CATALOG.filter((item) =>
                          item.operations.includes("chat"),
                        ).map((item) => (
                          <option key={item.model}>{item.model}</option>
                        ))}
                      </select>
                    </label>
                    <label className="block text-xs font-medium">
                      消息
                      <textarea
                        className="bg-muted/20 mt-1 min-h-28 w-full rounded-lg border p-2 text-sm"
                        value={chatPrompt}
                        onChange={(event) => setChatPrompt(event.target.value)}
                      />
                    </label>
                    <label className="flex items-center gap-2 text-xs">
                      <input
                        type="checkbox"
                        checked={chatStream}
                        onChange={(event) =>
                          setChatStream(event.target.checked)
                        }
                      />
                      模拟流式输出
                    </label>
                  </div>
                )}
                {tab === "image" && (
                  <div className="mt-4 space-y-3">
                    <div className="grid grid-cols-2 gap-1 rounded-lg bg-muted p-1">
                      {(
                        ["image_generation", "image_edits"] as ImageOperation[]
                      ).map((operation) => (
                        <button
                          key={operation}
                          className={cn(
                            "rounded-md px-2 py-2 text-xs",
                            imageOperation === operation
                              ? "bg-background shadow"
                              : "text-muted-foreground",
                          )}
                          onClick={() => setImageOperation(operation)}
                        >
                          {operation === "image_generation"
                            ? "文生图"
                            : "图生图"}
                        </button>
                      ))}
                    </div>
                    <label className="block text-xs font-medium">
                      模型
                      <select
                        className="bg-background mt-1 h-9 w-full rounded-lg border px-2 font-mono text-xs"
                        value={activeWorkbenchModel}
                        onChange={(event) =>
                          setSelectedModel(event.target.value)
                        }
                      >
                        {LOCAL_CATALOG.filter((item) =>
                          item.operations.includes(imageOperation),
                        ).map((item) => (
                          <option key={item.model}>{item.model}</option>
                        ))}
                      </select>
                    </label>
                    <label className="block text-xs font-medium">
                      提示词
                      <textarea
                        className="bg-muted/20 mt-1 min-h-24 w-full rounded-lg border p-2 text-sm"
                        value={imagePrompt}
                        onChange={(event) => setImagePrompt(event.target.value)}
                      />
                    </label>
                    {imageOperation === "image_edits" && (
                      <div className="rounded-lg border border-dashed p-3 text-xs">
                        <div className="font-medium">固定参考图</div>
                        <div className="text-muted-foreground mt-1">
                          {LOCAL_IMAGE_FIXTURE} · 256×256 PNG · multipart
                        </div>
                      </div>
                    )}
                    <div className="grid grid-cols-2 gap-2">
                      <label className="text-xs">
                        比例
                        <select
                          className="bg-background mt-1 h-8 w-full rounded border px-1 text-xs"
                          value={imageRatio}
                          onChange={(event) =>
                            setImageRatio(event.target.value)
                          }
                        >
                          <option>1:1</option>
                          <option>16:9</option>
                          <option>9:16</option>
                          <option>4:3</option>
                          <option>3:4</option>
                        </select>
                      </label>
                      <label className="text-xs">
                        分辨率
                        <select
                          className="bg-background mt-1 h-8 w-full rounded border px-1 text-xs"
                          value={imageResolution}
                          onChange={(event) =>
                            setImageResolution(event.target.value)
                          }
                        >
                          <option>1024x1024</option>
                          <option>1536x1024</option>
                          <option>1024x1536</option>
                        </select>
                      </label>
                      <label className="text-xs">
                        质量
                        <select
                          className="bg-background mt-1 h-8 w-full rounded border px-1 text-xs"
                          value={imageQuality}
                          onChange={(event) =>
                            setImageQuality(event.target.value)
                          }
                        >
                          <option>auto</option>
                          <option>medium</option>
                          <option>high</option>
                        </select>
                      </label>
                      <label className="text-xs">
                        数量
                        <select
                          className="bg-background mt-1 h-8 w-full rounded border px-1 text-xs"
                          value={imageQuantity}
                          onChange={(event) =>
                            setImageQuantity(event.target.value)
                          }
                        >
                          <option>1</option>
                          <option>2</option>
                          <option>3</option>
                          <option>4</option>
                        </select>
                      </label>
                    </div>
                  </div>
                )}
                {tab === "video" && (
                  <div className="mt-4 space-y-3">
                    <label className="block text-xs font-medium">
                      模型
                      <div className="bg-muted/40 mt-1 rounded-lg border px-3 py-2 font-mono text-xs">
                        minimax-h3
                      </div>
                    </label>
                    <label className="block text-xs font-medium">
                      提示词
                      <textarea
                        className="bg-muted/20 mt-1 min-h-24 w-full rounded-lg border p-2 text-sm"
                        value={videoPrompt}
                        onChange={(event) => setVideoPrompt(event.target.value)}
                      />
                    </label>
                    <div className="grid grid-cols-2 gap-2">
                      <label className="text-xs">
                        比例
                        <select
                          className="bg-background mt-1 h-8 w-full rounded border px-1 text-xs"
                          value={videoRatio}
                          onChange={(event) =>
                            setVideoRatio(event.target.value)
                          }
                        >
                          <option>16:9</option>
                          <option>9:16</option>
                          <option>1:1</option>
                        </select>
                      </label>
                      <label className="text-xs">
                        分辨率
                        <select
                          className="bg-background mt-1 h-8 w-full rounded border px-1 text-xs"
                          value={videoResolution}
                          onChange={(event) =>
                            setVideoResolution(event.target.value)
                          }
                        >
                          <option>2K</option>
                          <option>1K</option>
                        </select>
                      </label>
                      <label className="text-xs">
                        时长
                        <select
                          className="bg-background mt-1 h-8 w-full rounded border px-1 text-xs"
                          value={videoDuration}
                          onChange={(event) =>
                            setVideoDuration(event.target.value)
                          }
                        >
                          <option>5</option>
                          <option>6</option>
                          <option>10</option>
                          <option>15</option>
                        </select>
                      </label>
                    </div>
                  </div>
                )}
                {(tab === "chat" || tab === "image" || tab === "video") && (
                  <div className="mt-5 space-y-2">
                    <label className="block text-xs font-medium">
                      模拟场景
                      <select
                        className="bg-background mt-1 h-9 w-full rounded-lg border px-2 text-xs"
                        value={scenario}
                        onChange={(event) =>
                          setScenario(event.target.value as FakeScenario)
                        }
                      >
                        {LOCAL_SCENARIOS.map((item) => (
                          <option key={item.value} value={item.value}>
                            {item.label}
                          </option>
                        ))}
                      </select>
                    </label>
                    <div className="flex gap-2">
                      <Button
                        className="flex-1"
                        disabled={Boolean(activeTaskId)}
                        onClick={() => {
                          if (tab === "chat")
                            void execute(
                              "chat",
                              activeWorkbenchModel,
                              chatStream ? "聊天 · 流式" : "聊天 · 非流式",
                            );
                          else if (tab === "image")
                            void execute(
                              imageOperation,
                              activeWorkbenchModel,
                              imageOperationLabel(imageOperation),
                            );
                          else
                            void execute(
                              "video_generation",
                              "minimax-h3",
                              `视频 · ${videoDuration}s`,
                            );
                        }}
                      >
                        <Play className="size-4" />
                        执行
                      </Button>
                      <Button
                        variant="outline"
                        size="icon"
                        disabled={!activeTaskId}
                        aria-label="停止"
                        onClick={stopActive}
                      >
                        <Square className="size-4" />
                      </Button>
                    </div>
                  </div>
                )}
              </aside>
              <section className="bg-background min-h-[620px] rounded-2xl border p-5 shadow-sm">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <div className="text-muted-foreground text-xs">
                      当前工作台 ·{" "}
                      {tab === "image"
                        ? imageOperationLabel(imageOperation)
                        : tabs.find((item) => item.value === tab)?.label}
                    </div>
                    <h2 className="mt-1 text-lg font-semibold">
                      {activeWorkbenchModel}
                    </h2>
                  </div>
                  <Badge variant="secondary">fake-upstream</Badge>
                </div>
                <div className="mt-5 flex min-h-[470px] items-center justify-center rounded-2xl border border-dashed bg-[radial-gradient(circle_at_center,rgba(45,212,191,.12),transparent_55%)] p-6">
                  {result?.kind === "image" && (
                    <img
                      src={result.value}
                      alt="本地模拟生成结果"
                      className="max-h-[430px] max-w-full rounded-xl shadow-lg"
                    />
                  )}
                  {result?.kind === "video" && (
                    <div className="w-full max-w-3xl space-y-3">
                      <img
                        src={result.value}
                        alt="本地模拟视频封面"
                        className="w-full rounded-xl shadow-lg"
                      />
                      <div className="text-muted-foreground text-center text-xs">
                        任务创建一次 · GET 轮询 · 客户任务 ID 与上游任务 ID
                        已隔离
                      </div>
                    </div>
                  )}
                  {result?.kind === "text" && (
                    <div className="max-w-2xl rounded-xl border bg-muted/40 p-5 text-sm leading-7">
                      {result.value}
                    </div>
                  )}
                  {result?.kind === "error" && (
                    <div className="max-w-2xl rounded-xl border border-destructive/30 bg-destructive/5 p-5 text-sm leading-7">
                      <div className="text-destructive font-semibold">
                        本地模拟失败
                      </div>
                      <div className="mt-2">{result.value}</div>
                      <div className="text-muted-foreground mt-3 text-xs">
                        不会自动降级、不会重试、不会切换模型。
                      </div>
                    </div>
                  )}
                  {!result && (
                    <div className="text-muted-foreground text-center text-sm">
                      <Sparkles className="mx-auto mb-3 size-8 opacity-40" />
                      <p>选择参数后执行本地模拟</p>
                      <p className="mt-1 text-xs">
                        所有请求仅在浏览器 fixture 内完成
                      </p>
                    </div>
                  )}
                </div>
                {result?.meta && (
                  <div className="bg-muted/30 mt-4 grid gap-2 rounded-xl border p-3 font-mono text-[11px] md:grid-cols-4">
                    <span>endpoint: {result.meta.endpoint}</span>
                    <span>shape: {result.meta.request_shape}</span>
                    <span>upstream: {String(result.meta.upstream_called)}</span>
                    <span>
                      billing: {String(result.meta.simulated_billing)}
                    </span>
                  </div>
                )}
              </section>
              <aside className="bg-background rounded-2xl border p-4 shadow-sm">
                <div className="flex items-center justify-between">
                  <h2 className="font-semibold">最近任务</h2>
                  <Badge variant="outline">{tasks.length}</Badge>
                </div>
                <div className="mt-3 space-y-2">
                  {tasks.length === 0 && (
                    <p className="text-muted-foreground py-8 text-center text-xs">
                      暂无本地任务
                    </p>
                  )}
                  {tasks.map((task) => (
                    <button
                      key={task.id}
                      className="hover:bg-muted/40 w-full rounded-xl border p-3 text-left"
                      onClick={() =>
                        setResult({
                          kind:
                            task.final_status === "failed" ? "error" : "text",
                          value:
                            task.final_status === "failed"
                              ? `${task.stage}（HTTP ${task.status ?? "timeout"}）`
                              : `${task.title} · ${task.final_status}`,
                          meta: task,
                        })
                      }
                    >
                      <div className="flex items-start gap-2">
                        {statusIcon(task.final_status)}
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-xs font-medium">
                            {task.title}
                          </div>
                          <div className="text-muted-foreground mt-1 truncate font-mono text-[10px]">
                            {task.endpoint}
                          </div>
                        </div>
                      </div>
                      <div className="text-muted-foreground mt-2 flex justify-between text-[10px]">
                        <span>{task.final_status}</span>
                        <span>{task.duration_ms}ms</span>
                      </div>
                    </button>
                  ))}
                </div>
                <div className="mt-5 border-t pt-4">
                  <h3 className="text-xs font-semibold">请求证据</h3>
                  <div className="mt-2 space-y-1.5">
                    {events.slice(0, 5).map((event) => (
                      <div
                        key={event.id}
                        className="rounded-lg bg-muted/30 p-2 font-mono text-[10px]"
                      >
                        <div>
                          {event.operation} · {event.status ?? "—"} ·{" "}
                          {event.stage}
                        </div>
                        <div className="text-muted-foreground mt-1">
                          retry={String(event.retried)} · upstream=
                          {String(event.upstream_called)} · billing=
                          {String(event.simulated_billing)}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
                {tab === "tasks" && (
                  <div className="mt-5 border-t pt-4">
                    <div className="grid grid-cols-2 gap-2 text-center text-xs">
                      <div className="bg-muted/30 rounded-lg p-2">
                        <div className="text-muted-foreground">运行中</div>
                        <div className="mt-1 font-semibold">
                          {
                            tasks.filter(
                              (task) => task.final_status === "running",
                            ).length
                          }
                        </div>
                      </div>
                      <div className="bg-muted/30 rounded-lg p-2">
                        <div className="text-muted-foreground">需注意</div>
                        <div className="mt-1 font-semibold">
                          {
                            tasks.filter(
                              (task) => task.final_status === "failed",
                            ).length
                          }
                        </div>
                      </div>
                    </div>
                  </div>
                )}
              </aside>
            </div>
          </section>
        )}
      </div>
    </main>
  );
}

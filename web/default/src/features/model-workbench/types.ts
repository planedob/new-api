export type WorkbenchTab = 'chat' | 'image' | 'video' | 'tasks'
export type WorkbenchSurface = 'catalog' | 'workbench'
export type CatalogView = 'cards' | 'table'
export type ImageOperation = 'image_generation' | 'image_edits'
export type FakeScenario =
  | 'success'
  | 'bad_request'
  | 'rate_limit'
  | 'server_error'
  | 'timeout'
  | 'empty_result'
  | 'cancelled'
  | 'safety_rejected'
  | 'accepted_disconnect'

export type CatalogModel = {
  model: string
  vendor: string
  group: string
  operations: string[]
  parameter_bounds: Record<string, unknown>
  cataloged: boolean
  selectable: boolean
  testable: boolean
  verified: 'local_fixture' | 'unknown'
  verification_scope: 'LOCAL_FIXTURE'
  price_summary: string
  performance: { latency_ms: number | null; success_rate: number | null; throughput: string }
  tags: string[]
  endpoint_type: string
}

export type LocalRequestEvent = {
  id: string
  operation: string
  endpoint: string
  method: 'POST' | 'GET'
  model: string
  scenario: FakeScenario
  request_shape: 'json' | 'multipart' | 'poll'
  upstream_called: boolean
  retried: boolean
  simulated_billing: boolean
  status: number | null
  stage: string
  final_status: 'running' | 'completed' | 'failed' | 'cancelled'
  duration_ms: number
  created_at: string
}

export type WorkbenchTask = LocalRequestEvent & { title: string }

export type FakeResponse = {
  request: LocalRequestEvent
  text?: string
  image_url?: string
  video_url?: string
  task_id?: string
}

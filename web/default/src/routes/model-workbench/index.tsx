import { createFileRoute } from '@tanstack/react-router'
import { ModelWorkbench } from '@/features/model-workbench'

export const Route = createFileRoute('/model-workbench/')({ component: ModelWorkbench })

import z from 'zod'
import { createFileRoute } from '@tanstack/react-router'
import { Pricing } from '@/features/pricing'
import { ModelWorkbench } from '@/features/model-workbench'

const pricingSearchSchema = z.object({
  search: z.string().optional(),
  sort: z.string().optional(),
  vendor: z.string().optional(),
  group: z.string().optional(),
  quotaType: z.string().optional(),
  endpointType: z.string().optional(),
  tag: z.string().optional(),
  tokenUnit: z.enum(['M', 'K']).optional(),
  view: z.enum(['card', 'table']).optional().catch(undefined),
  rechargePrice: z.boolean().optional(),
})

function PricingPage() {
  const localModelSquareEnabled =
    import.meta.env.VITE_MODEL_WORKBENCH_LOCAL_FIXTURE === 'true' &&
    typeof window !== 'undefined' &&
    ['127.0.0.1', 'localhost', '[::1]'].includes(window.location.hostname)

  return localModelSquareEnabled ? <ModelWorkbench /> : <Pricing />
}

export const Route = createFileRoute('/pricing/')({
  validateSearch: pricingSearchSchema,
  component: PricingPage,
})

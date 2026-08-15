import { useEffect, useState } from 'react'
import { ImageIcon, SendIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { ModelGroupSelector } from '@/components/model-group-selector'
import { getImage2ErrorDetails, sendImage2Request } from '../api'
import {
  image2DefaultSelection,
  image2MaxN,
  image2Qualities,
  image2Sizes,
  isImage2SelectionSupported,
} from '../lib/image2-capabilities'
import { nextImage2ModeState } from '../lib/image2-mode'
import type {
  GroupOption,
  Image2CapabilityCatalog,
  Image2Mode,
  Image2Result,
  ModelOption,
} from '../types'

interface Image2PlaygroundProps {
  disabled: boolean
  groupValue: string
  groups: GroupOption[]
  modelValue: string
  models: ModelOption[]
  onGroupChange: (value: string) => void
  onModelChange: (value: string) => void
  capability?: Image2CapabilityCatalog
}

export function Image2Playground(props: Image2PlaygroundProps) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<Image2Mode>('generations')
  const [prompt, setPrompt] = useState('')
  const [image, setImage] = useState<File | null>(null)
  const [size, setSize] = useState('auto')
  const [quality, setQuality] = useState('auto')
  const [count, setCount] = useState(1)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [result, setResult] = useState<Image2Result | null>(null)

  const sizes = image2Sizes(props.capability, mode)
  const qualityOptions = image2Qualities(props.capability, mode, size)
  const maxN = image2MaxN(
    props.capability,
    mode,
    size,
    quality as 'auto' | 'standard' | 'high'
  )
  const hasCapability = Boolean(props.capability && sizes.length > 0)

  useEffect(() => {
    const defaultSelection = image2DefaultSelection(props.capability, mode)
    if (!defaultSelection) return
    const nextQualityOptions = image2Qualities(props.capability, mode, size)
    if (
      !sizes.includes(size) ||
      !nextQualityOptions.includes(quality as 'auto' | 'standard' | 'high')
    ) {
      setSize(defaultSelection.size)
      setQuality(defaultSelection.quality)
      setCount(defaultSelection.n)
      return
    }
    const nextMaxN = image2MaxN(
      props.capability,
      mode,
      size,
      quality as 'auto' | 'standard' | 'high'
    )
    if (nextMaxN > 0 && count > nextMaxN) setCount(nextMaxN)
  }, [props.capability, mode, size, quality, count])

  const isEdit = mode === 'edits'
  const canSubmit =
    hasCapability &&
    prompt.trim().length > 0 &&
    (!isEdit || image !== null) &&
    isImage2SelectionSupported(props.capability, mode, {
      size,
      quality: quality as 'auto' | 'standard' | 'high',
      n: count,
    })

  function switchMode(nextMode: Image2Mode) {
    const nextState = nextImage2ModeState(image, nextMode)
    setMode(nextState.mode)
    setImage(nextState.image)
  }

  async function submit() {
    if (!canSubmit || props.disabled) return
    setIsSubmitting(true)
    setResult(null)
    try {
      setResult(
        await sendImage2Request(
          {
            model: props.modelValue,
            group: props.groupValue,
            prompt: prompt.trim(),
            ...(size === 'auto' ? {} : { size }),
            ...(quality === 'auto' ? {} : { quality }),
            n: count,
          },
          mode,
          image ?? undefined
        )
      )
    } catch (error: unknown) {
      const details = getImage2ErrorDetails(error, t('Request error occurred'))
      setResult({
        mode,
        requestId: details.requestId,
        images: [],
        error: details.message,
        errorCode: details.code ?? null,
        errorMetadata: details.metadata ?? null,
      })
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <div className='mx-auto flex size-full w-full max-w-4xl flex-col overflow-y-auto p-4 md:p-6'>
      <Card>
        <CardHeader>
          <CardTitle className='flex items-center gap-2'>
            <ImageIcon size={18} />
            {t('Image2 test')}
          </CardTitle>
          <CardDescription>
            {t(
              'Use the native image endpoints. Generation and image editing are submitted separately and every result exposes its request ID.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='grid gap-5'>
          <ModelGroupSelector
            selectedModel={props.modelValue}
            models={props.models}
            onModelChange={props.onModelChange}
            selectedGroup={props.groupValue}
            groups={props.groups}
            onGroupChange={props.onGroupChange}
            disabled={props.disabled || isSubmitting}
          />
          <div className='grid gap-2 sm:grid-cols-2'>
            <Button
              variant={isEdit ? 'outline' : 'secondary'}
              onClick={() => switchMode('generations')}
              disabled={
                props.disabled ||
                isSubmitting ||
                !props.capability?.operations.includes('generations')
              }
            >
              {t('Text to image')} · /images/generations
            </Button>
            <Button
              variant={isEdit ? 'secondary' : 'outline'}
              onClick={() => switchMode('edits')}
              disabled={
                props.disabled ||
                isSubmitting ||
                !props.capability?.operations.includes('edits')
              }
            >
              {t('Image to image')} · /images/edits
            </Button>
          </div>
          <div className='grid gap-2'>
            <Label htmlFor='image2-prompt'>{t('Prompt')}</Label>
            <Textarea
              id='image2-prompt'
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              disabled={props.disabled || isSubmitting}
              placeholder={t('Describe the image you want to create')}
            />
          </div>
          {isEdit && (
            <div className='grid gap-2'>
              <Label htmlFor='image2-source'>{t('Source image')}</Label>
              <Input
                id='image2-source'
                type='file'
                accept='image/*'
                disabled={props.disabled || isSubmitting}
                onChange={(event) => setImage(event.target.files?.[0] ?? null)}
              />
              <p className='text-muted-foreground text-xs'>
                {image
                  ? image.name
                  : t(
                      'A reference image is required for image-to-image testing.'
                    )}
              </p>
            </div>
          )}
          <div className='grid gap-4 sm:grid-cols-3'>
            <label className='grid gap-2 text-sm font-medium'>
              {t('Size')}
              <select
                className='border-input h-8 rounded-lg border bg-transparent px-2.5 text-sm'
                value={size}
                onChange={(event) => setSize(event.target.value)}
                disabled={props.disabled || isSubmitting || !hasCapability}
              >
                {sizes.map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
              </select>
            </label>
            <label className='grid gap-2 text-sm font-medium'>
              {t('Quality')}
              <select
                className='border-input h-8 rounded-lg border bg-transparent px-2.5 text-sm'
                value={quality}
                onChange={(event) =>
                  setQuality(event.target.value as 'auto' | 'standard' | 'high')
                }
                disabled={props.disabled || isSubmitting || !hasCapability}
              >
                {qualityOptions.map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
              </select>
            </label>
            <label className='grid gap-2 text-sm font-medium'>
              {t('Count')}
              <Input
                type='number'
                min={1}
                max={maxN || 1}
                value={count}
                disabled={props.disabled || isSubmitting || !hasCapability}
                onChange={(event) =>
                  setCount(
                    Math.min(
                      maxN || 1,
                      Math.max(1, Number(event.target.value) || 1)
                    )
                  )
                }
              />
            </label>
          </div>
          <p className='text-muted-foreground text-xs'>
            {hasCapability
              ? t(
                  'Options are loaded from the selected group capability. No automatic size or quality is synthesized; every submitted combination is declared supported.'
                )
              : t(
                  'No supported Image2 combination is currently available for this group.'
                )}
          </p>
          <Button
            className='w-full'
            onClick={submit}
            disabled={props.disabled || isSubmitting || !canSubmit}
          >
            <SendIcon />
            {isSubmitting
              ? t('Submitting…')
              : isEdit
                ? t('Test image to image')
                : t('Test text to image')}
          </Button>
        </CardContent>
      </Card>
      {result && (
        <Card className='mt-4'>
          <CardHeader>
            <CardTitle>
              {result.error
                ? t('Image request failed')
                : t('Image request result')}
            </CardTitle>
            <CardDescription>
              {result.mode === 'edits'
                ? '/images/edits'
                : '/images/generations'}{' '}
              · {t('Request ID')}:{' '}
              <code>{result.requestId ?? t('Not returned')}</code>
            </CardDescription>
          </CardHeader>
          <CardContent>
            {result.error ? (
              <p className='text-destructive'>{result.error}</p>
            ) : (
              <div className='grid gap-3 sm:grid-cols-2'>
                {result.images.map((source) => (
                  <img
                    key={source}
                    src={source}
                    alt={t('Generated image')}
                    className='w-full rounded-lg border'
                  />
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  )
}

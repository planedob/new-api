import type { Image2Mode } from '../types'

export interface Image2ModeState {
  mode: Image2Mode
  image: File | null
}

/**
 * A generation request must never retain an edit source. Keeping this pure
 * makes the invariant testable without a browser renderer and lets the UI
 * clear both the selected File and any source preview in one transition.
 */
export function nextImage2ModeState(
  image: File | null,
  nextMode: Image2Mode
): Image2ModeState {
  return {
    mode: nextMode,
    image: nextMode === 'generations' ? null : image,
  }
}

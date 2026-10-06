export interface PollerOptions<T> {
  /** Fetches the current value. */
  fetch: () => Promise<T>
  /** Delay between the end of one poll and the start of the next. */
  interval: number
  /** Multiplier for the delay while the page is hidden. */
  hiddenFactor?: number
  isHidden?: () => boolean
  onResult: (result: { value: T } | { error: unknown }) => void
}

/**
 * Poller fetches a value periodically. Polls never overlap: a refresh while a poll is in
 * flight supersedes it, its result is discarded and only the newest poll schedules the next
 * one, so there is exactly one polling cycle at any time.
 */
export class Poller<T> {
  private generation = 0
  private timer: ReturnType<typeof setTimeout> | undefined
  private stopped = false

  constructor(private readonly opts: PollerOptions<T>) {}

  /** Polls now and restarts the cycle. */
  async refresh(): Promise<void> {
    if (this.stopped) return
    const generation = ++this.generation
    clearTimeout(this.timer)
    this.timer = undefined
    let result: { value: T } | { error: unknown }
    try {
      result = { value: await this.opts.fetch() }
    } catch (error) {
      result = { error }
    }
    if (this.stopped || generation !== this.generation) return
    this.opts.onResult(result)
    const hidden = this.opts.isHidden?.() ?? false
    this.timer = setTimeout(() => void this.refresh(), this.opts.interval * (hidden ? this.opts.hiddenFactor ?? 1 : 1))
  }

  stop(): void {
    this.stopped = true
    clearTimeout(this.timer)
    this.timer = undefined
  }
}

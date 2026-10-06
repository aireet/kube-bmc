import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Poller } from './poller'

/** A fetch whose calls resolve only when the test says so. */
function controlledFetch() {
  const pending: Array<(v: number) => void> = []
  let calls = 0
  return {
    fetch: () => {
      calls++
      return new Promise<number>((resolve) => pending.push(resolve))
    },
    resolve: (i: number, v: number) => pending[i](v),
    get calls() {
      return calls
    },
  }
}

const flush = () => vi.advanceTimersByTimeAsync(0)

describe('Poller', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('polls on the interval', async () => {
    const fetch = vi.fn(async () => 1)
    const results: unknown[] = []
    const p = new Poller({ fetch, interval: 1000, onResult: (r) => results.push(r) })
    await p.refresh()
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetch).toHaveBeenCalledTimes(4)
    expect(results).toHaveLength(4)
    p.stop()
  })

  it('keeps a single cycle when refreshed during a poll', async () => {
    const f = controlledFetch()
    const results: unknown[] = []
    const p = new Poller({ fetch: f.fetch, interval: 1000, onResult: (r) => results.push(r) })
    void p.refresh() // poll 0 in flight
    void p.refresh() // manual refresh: poll 1 supersedes poll 0
    f.resolve(1, 11)
    await flush()
    f.resolve(0, 10) // the stale result arrives late
    await flush()
    expect(results).toEqual([{ value: 11 }])

    await vi.advanceTimersByTimeAsync(1000)
    expect(f.calls).toBe(3) // one follow-up poll, not two
    f.resolve(2, 12)
    await flush()
    await vi.advanceTimersByTimeAsync(1000)
    expect(f.calls).toBe(4)
    p.stop()
  })

  it('reports errors and keeps polling', async () => {
    let n = 0
    const results: unknown[] = []
    const p = new Poller({
      fetch: async () => {
        if (n++ === 0) throw new Error('unavailable')
        return n
      },
      interval: 1000,
      onResult: (r) => results.push(r),
    })
    await p.refresh()
    await vi.advanceTimersByTimeAsync(1000)
    expect(results[0]).toMatchObject({ error: new Error('unavailable') })
    expect(results[1]).toEqual({ value: 2 })
    p.stop()
  })

  it('polls less often while hidden', async () => {
    const fetch = vi.fn(async () => 1)
    const p = new Poller({ fetch, interval: 1000, hiddenFactor: 4, isHidden: () => true, onResult: () => {} })
    await p.refresh()
    await vi.advanceTimersByTimeAsync(3999)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetch).toHaveBeenCalledTimes(2)
    p.stop()
  })

  it('stops', async () => {
    const f = controlledFetch()
    const onResult = vi.fn()
    const p = new Poller({ fetch: f.fetch, interval: 1000, onResult })
    void p.refresh()
    p.stop()
    f.resolve(0, 1)
    await vi.advanceTimersByTimeAsync(5000)
    expect(onResult).not.toHaveBeenCalled()
    expect(f.calls).toBe(1)
  })
})

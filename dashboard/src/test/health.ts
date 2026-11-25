import type { FakeRoute } from './fake-api'
import type { MetricsOutBody, ProcessesOutBody, SeriesView } from '@/api/gen'
import { capability } from './device-detail'

export const METRICS = capability({
  id: 'system.metrics',
  desired: { intervalS: 10 },
  desiredGen: 1,
  appliedGen: 1,
})

// points seconds before now, the newest last
export function series(name: string, attrs: Record<string, string>, values: readonly number[], stepS = 10): SeriesView {
  const now = Date.now()
  return {
    name,
    kind: 'gauge',
    attrs,
    points: values.map((value, i) => ({
      ts: new Date(now - (values.length - i) * stepS * 1000).toISOString(),
      value,
    })),
  }
}

export function metricsBody(names: readonly string[]): MetricsOutBody {
  const all: SeriesView[] = [
    series('system.cpu.utilization', { cpu: '0' }, [0.9, 0.95]),
    series('system.cpu.utilization', {}, [0.5, 0.25]),
    series('system.memory.used', {}, [400_000_000, 431_000_000]),
    series('system.memory.limit', {}, [2_080_000_000, 2_080_000_000]),
    series('system.filesystem.free', { mountpoint: '/data' }, [1_300_000_000]),
    series('system.filesystem.limit', { mountpoint: '/data' }, [4 * 1024 ** 3]),
    series('system.filesystem.free', { mountpoint: '/tmp' }, [100 * 1024 ** 2]),
    series('system.filesystem.limit', { mountpoint: '/tmp' }, [128 * 1024 ** 2]),
    series('system.temperature', {}, [46.5, 47.25]),
  ]
  return {
    series: all.filter(s => names.includes(s.name)),
    truncated: false,
  }
}

// answers each read with the series of the names it asked for
export const metricsRoute: FakeRoute = request => Response.json(metricsBody(new URL(request.url).searchParams.getAll('name')))

export const PROCESSES: ProcessesOutBody = {
  topCpu: [
    {
      pid: 812,
      comm: 'cadenced',
      cpu: 45.2,
      rss: 13_000_000,
    },
    {
      pid: 901,
      comm: '<b>soloist</b>',
      cpu: 12,
      rss: 61_000_000,
    },
  ],
  topRss: [
    {
      pid: 901,
      comm: '<b>soloist</b>',
      cpu: 12,
      rss: 61_000_000,
    },
    {
      pid: 812,
      comm: 'cadenced',
      cpu: 45.2,
      rss: 13_000_000,
    },
  ],
}

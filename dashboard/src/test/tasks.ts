import type { DetailJson, TaskJson } from '@/api/gen'
import { KITCHEN } from '@/test/household'

// a task as the server lists it; `question` is null while there is none
export function task(over: Partial<TaskJson> & Pick<TaskJson, 'shortId' | 'title' | 'status'>): TaskJson {
  return {
    kind: 'hermes',
    deviceId: KITCHEN,
    progress: '',
    errorCode: '',
    createdAt: '2026-11-12T09:00:00Z',
    updatedAt: '2026-11-12T09:05:00Z',
    finishedAt: null,
    reportedAt: null,
    question: null,
    ...over,
  }
}

// get-task answers the task's own fields beside its history
export function detail(of: TaskJson, over: Partial<DetailJson> = {}): DetailJson {
  return {
    ...of,
    result: '',
    events: [],
    notices: [],
    ...over,
  }
}

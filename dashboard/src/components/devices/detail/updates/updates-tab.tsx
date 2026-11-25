import type { AccessView, Detail, RunView } from '@/api/gen'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { PlugZapIcon, TriangleAlertIcon } from 'lucide-react'
import { useState } from 'react'
import { deleteDeviceArtifact, probeDeviceAccess, putDeviceAccess, rollbackDeviceOps, startDeviceOps } from '@/api/gen'
import { listDeviceAccessOptions, listDeviceArtifactsOptions, listDeviceOpsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { stampText } from '@/lib/format-time'
import { kindNeedsVersion, kindOfBoard, rollbackRun, runText, updateAvailable } from './updates-state'
import { uploadArtifact } from './upload-artifact'

// the owner's update tab: artifacts for the board, runs with their steps and output tails, and the
// out-of-band access (adb on the lan, ssh to the host) the server-driven installs use. every run
// needs a confirm (a restart interrupts what plays); agents never fire one (rule 7)
export function UpdatesTab({ device }: { device: Detail }) {
  const api = useApi()
  const queryClient = useQueryClient()
  const board = device.model
  const kind = kindOfBoard(board)
  const artifactsQuery = sharedKey(listDeviceArtifactsOptions({
    client: api,
    query: { board },
  }))
  const runsQuery = sharedKey(listDeviceOpsOptions({
    client: api,
    path: { id: device.id },
  }))
  const accessQuery = sharedKey(listDeviceAccessOptions({
    client: api,
    path: { id: device.id },
  }))
  const artifacts = useQuery({ ...artifactsQuery, refetchInterval: 30_000 })
  const runs = useQuery({ ...runsQuery, refetchInterval: 5000 })
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: artifactsQuery.queryKey })
    void queryClient.invalidateQueries({ queryKey: runsQuery.queryKey })
    void queryClient.invalidateQueries({ queryKey: accessQuery.queryKey })
  }
  const newest = (artifacts.data ?? [])[0]
  const lastGood = rollbackRun(runs.data ?? [])
  return (
    <div className="flex flex-col gap-8">
      <section className="rounded-lg border bg-card p-4 flex flex-col gap-3">
        <h3 className="font-medium">Daemon</h3>
        <p className="text-sm text-muted-foreground">
          running
          {' '}
          <span className="font-mono">{device.daemonVersion || 'unknown'}</span>
          {newest && (updateAvailable(device.daemonVersion, newest.version)
            ? (
                <>
                  {' '}
                  —
                  <span className="text-warning">
                    {newest.version}
                    {' '}
                    is published
                  </span>
                </>
              )
            : ' — up to date')}
        </p>
      </section>

      <section className="rounded-lg border bg-card p-4 flex flex-col gap-3">
        <h3 className="font-medium">
          Artifacts (
          {kind}
          )
        </h3>
        <UploadForm board={board} kind={kind} onDone={refresh} />
        <ul className="flex flex-col gap-2 text-sm">
          {(artifacts.data ?? []).map(artifact => (
            <li key={artifact.id} className="flex flex-wrap items-center gap-3 rounded-md border p-2">
              <span className="font-mono">{artifact.version}</span>
              {artifact.label && <span className="text-muted-foreground">{artifact.label}</span>}
              <span className="text-muted-foreground">
                {Math.ceil(artifact.size / 1024 / 1024)}
                {' '}
                MiB
              </span>
              <span className="text-muted-foreground">{stampText(artifact.createdAt, null)}</span>
              <span className="grow" />
              <UpdateButton device={device} artifactId={artifact.id} onDone={refresh} />
              <DeleteArtifact artifactId={artifact.id} onDone={refresh} />
            </li>
          ))}
          {artifacts.isSuccess && (artifacts.data ?? []).length === 0 && (
            <li className="text-muted-foreground">
              nothing published for
              {board}
              {' '}
              yet
            </li>
          )}
        </ul>
      </section>

      <section className="rounded-lg border bg-card p-4 flex flex-col gap-3">
        <h3 className="font-medium">Runs</h3>
        {lastGood && <RollbackButton onDone={refresh} run={lastGood} />}
        <ul className="flex flex-col gap-3 text-sm">
          {(runs.data ?? []).map(run => <RunRow key={run.id} run={run} />)}
          {runs.isSuccess && (runs.data ?? []).length === 0 && (
            <li className="text-muted-foreground">no runs yet</li>
          )}
        </ul>
      </section>

      <Access device={device} onRefresh={refresh} />
    </div>
  )
}

function UpdateButton({ device, artifactId, onDone }: { device: Detail, artifactId: string, onDone: () => void }) {
  const api = useApi()
  const [transport, setTransport] = useState<'auto' | 'link' | 'adb' | 'ssh'>('auto')
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState('')
  const run = useMutation({
    mutationFn: (chosen: 'auto' | 'link' | 'adb' | 'ssh') => startDeviceOps({
      client: api,
      path: { id: device.id },
      body: { artifactId, transport: chosen },
      throwOnError: false,
    }),
    onSuccess: (result) => {
      setError(result.error
        ? result.error.toString()
        : '')
      if (!result.error) {
        setConfirming(false)
        onDone()
      }
    },
  })
  return (
    <span className="flex items-center gap-2">
      <select
        aria-label="transport"
        className="rounded-md border bg-background px-2 py-1 text-xs"
        value={transport}
        onChange={e => setTransport(e.target.value as 'auto' | 'link' | 'adb' | 'ssh')}
      >
        <option value="auto">auto</option>
        <option value="link">link</option>
        <option value="adb">adb</option>
        <option value="ssh">ssh</option>
      </select>
      {confirming
        ? (
            <span className="flex items-center gap-1">
              <Button size="sm" disabled={run.isPending} onClick={() => run.mutate(transport)}>
                restarts the device — sure?
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>no</Button>
            </span>
          )
        : <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>update</Button>}
      {run.isPending && <span className="text-muted-foreground">starting…</span>}
      {error !== '' && <span className="text-destructive">{error}</span>}
    </span>
  )
}

function DeleteArtifact({ artifactId, onDone }: { artifactId: string, onDone: () => void }) {
  const api = useApi()
  const del = useMutation({
    mutationFn: () => deleteDeviceArtifact({
      client: api,
      path: { id: artifactId },
      throwOnError: true,
    }),
    onSuccess: onDone,
  })
  return <Button size="sm" variant="ghost" disabled={del.isPending} onClick={() => del.mutate()}>delete</Button>
}

function RollbackButton({ run, onDone }: { run: RunView, onDone: () => void }) {
  const api = useApi()
  const [confirming, setConfirming] = useState(false)
  const back = useMutation({
    mutationFn: () => rollbackDeviceOps({
      client: api,
      path: { runId: run.id },
      throwOnError: false,
    }),
    onSuccess: () => {
      setConfirming(false)
      onDone()
    },
  })
  return confirming
    ? (
        <span className="flex items-center gap-1">
          <Button size="sm" disabled={back.isPending} onClick={() => back.mutate()}>
            install
            {' '}
            {run.targetVersion}
            {' '}
            again — sure?
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>no</Button>
        </span>
      )
    : <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>rollback</Button>
}

function RunRow({ run }: { run: RunView }) {
  const [open, setOpen] = useState(false)
  const failed = run.status === 'failed' || run.status === 'needs_attention'
  return (
    <li className="rounded-md border">
      <button type="button" className="flex w-full flex-wrap items-center gap-3 p-2 text-left" onClick={() => setOpen(!open)}>
        <span className={`font-mono text-xs ${failed ? 'text-destructive' : ''}`}>{runText(run.status)}</span>
        <span className="font-mono">{run.targetVersion}</span>
        <span className="text-muted-foreground">{run.transport}</span>
        <span className="text-muted-foreground">{stampText(run.startedAt, null)}</span>
        {run.errorCode && <span className="text-destructive text-xs">{run.errorCode}</span>}
        <span className="grow" />
        <span className="text-muted-foreground text-xs">
          {(run.steps ?? []).length}
          {' '}
          steps
        </span>
      </button>
      {open && (
        <ol className="flex flex-col gap-1 border-t p-2 font-mono text-xs">
          {(run.steps ?? []).map(step => (
            <li key={step.index} className="flex flex-col gap-0.5">
              <span className={step.state === 'failed'
                ? 'text-destructive'
                : undefined}
              >
                {step.index}
                {' '}
                {step.state}
                {' '}
                {step.name}
                {step.exitCode
                  ? ` (exit ${step.exitCode})`
                  : ''}
                {step.errorCode
                  ? ` ${step.errorCode}`
                  : ''}
              </span>
              {step.outputTail && <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded bg-muted p-1">{step.outputTail}</pre>}
            </li>
          ))}
        </ol>
      )}
    </li>
  )
}

function UploadForm({ board, kind, onDone }: { board: string, kind: string, onDone: () => void }) {
  const [label, setLabel] = useState('')
  const [version, setVersion] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const needs = kindNeedsVersion(kind as 'tar' | 'apk' | 'deb')
  async function submit() {
    if (!file)
      return
    setBusy(true)
    setError('')
    try {
      await uploadArtifact({
        board,
        kind,
        label,
        version: needs ? version : undefined,
        file,
      })
      setFile(null)
      setLabel('')
      setVersion('')
      onDone()
    }
    catch (cause) {
      setError(String(cause))
    }
    finally {
      setBusy(false)
    }
  }
  return (
    // method="post" first, the whole opening tag one line: the form guard reads it off the opening
    // line, and the style rules want a tag that spans lines to carry one prop each
    // eslint-disable-next-line style/max-statements-per-line
    <form method="post" className="flex flex-wrap items-center gap-2 text-sm" onSubmit={(event) => { event.preventDefault(); void submit() }}>
      <input
        aria-label="artifact file"
        type="file"
        className="text-xs"
        onChange={e => setFile(e.target.files?.[0] ?? null)}
      />
      <Input aria-label="label" placeholder="label" className="w-32" value={label} onChange={e => setLabel(e.target.value)} />
      {needs && <Input aria-label="version" placeholder="version" className="w-36 font-mono" value={version} onChange={e => setVersion(e.target.value)} />}
      <Button type="submit" size="sm" disabled={busy || !file || (needs && version === '')}>upload</Button>
      {error !== '' && <span className="text-destructive">{error}</span>}
    </form>
  )
}

function AccessRow({ device, row, kind, onRefresh }: {
  device: Detail
  row?: AccessView
  kind: 'adb' | 'ssh'
  onRefresh: () => void
}) {
  const api = useApi()
  const [addr, setAddr] = useState(row?.addr ?? '')
  const [key, setKey] = useState('')
  const [pin, setPin] = useState(row?.hostKey ?? '')
  const [error, setError] = useState('')
  const save = useMutation({
    mutationFn: () => putDeviceAccess({
      client: api,
      path: { id: device.id, kind },
      body: { addr, sshKey: key || undefined, sshHostKey: pin || undefined },
      throwOnError: false,
    }),
    onSuccess: (result) => {
      setError(result.error
        ? result.error.toString()
        : '')
      onRefresh()
    },
  })
  const probe = useMutation({
    mutationFn: () => probeDeviceAccess({
      client: api,
      path: { id: device.id, kind },
      throwOnError: false,
    }),
    onSuccess: (result) => {
      setError(result.error
        ? result.error.toString()
        : '')
    },
  })
  return (
    <li className="flex flex-col gap-2 rounded-md border p-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="w-8 font-mono text-xs">{kind}</span>
        <Input
          aria-label={`${kind} address`}
          placeholder={kind === 'adb'
            ? '192.168.1.156:5555'
            : 'root@192.168.0.190:22'}
          className="w-64 font-mono text-xs"
          value={addr}
          onChange={e => setAddr(e.target.value)}
        />
        {row?.lastOk === false && (
          <span className="flex items-center gap-1 text-xs text-warning">
            <TriangleAlertIcon aria-hidden="true" className="size-3" />
            {' '}
            last probe failed
          </span>
        )}
        <Button size="sm" variant="outline" disabled={save.isPending} onClick={() => save.mutate()}>save</Button>
        <Button size="sm" variant="ghost" disabled={probe.isPending} onClick={() => probe.mutate()}>
          <PlugZapIcon aria-hidden="true" className="size-3" />
          {' '}
          test
        </Button>
      </div>
      {kind === 'ssh' && (
        <div className="flex flex-wrap gap-2">
          <Input aria-label="ssh key" placeholder="ssh private key (sealed, only when replacing)" className="w-64 font-mono text-xs" value={key} onChange={e => setKey(e.target.value)} />
          <Input aria-label="host key pin" placeholder="host key pin (empty pins the first seen)" className="w-64 font-mono text-xs" value={pin} onChange={e => setPin(e.target.value)} />
        </div>
      )}
      {error !== '' && <span className="text-destructive text-xs">{error}</span>}
    </li>
  )
}

function Access({ device, onRefresh }: { device: Detail, onRefresh: () => void }) {
  const api = useApi()
  const rows = useQuery(sharedKey(listDeviceAccessOptions({
    client: api,
    path: { id: device.id },
  })))
  const of = (kind: string) => (rows.data ?? []).find(row => row.kind === kind)
  return (
    <section className="rounded-lg border bg-card p-4 flex flex-col gap-3">
      <h3 className="font-medium">Access</h3>
      <p className="text-xs text-muted-foreground">
        how the server reaches this device out of band: adb over the lan (the tv's only way), ssh to its host. an empty address removes the row.
      </p>
      <ul className="flex flex-col gap-2">
        <AccessRow device={device} kind="adb" row={of('adb')} onRefresh={onRefresh} />
        <AccessRow device={device} kind="ssh" row={of('ssh')} onRefresh={onRefresh} />
      </ul>
    </section>
  )
}

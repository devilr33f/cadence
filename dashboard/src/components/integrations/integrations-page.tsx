import type { ReactNode } from 'react'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { useApi } from '@/api/use-api'
import { PageHeader } from '@/components/data/page-header'
import { devicesQueryOptions } from '@/lib/devices'
import { useLiveTopics } from '@/lib/live/use-live'
import { BusySection } from './busy/busy-section'
import { HermesStatus } from './hermes-status'
import { HomeAssistantSection } from './home-assistant/ha-section'
import { McpSection } from './mcp/mcp-section'
import { SubscriptionsSection } from './subscriptions/subscriptions-section'

// mcp_connected, mcp_unavailable and mcp_tools_changed refresh the servers and their tools; subscription_changed the
// subscriptions (presence, for the busy signals, is the shell's); home assistant and hermes have no topic
const TOPICS: readonly Topic[] = ['agent', 'subscriptions']

function Section({ title, description, children }: { title: string, description: string, children: ReactNode }) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-4">
      <div>
        <h2 id={titleId} className="text-lg font-semibold">{title}</h2>
        <p className="max-w-[65ch] text-base text-muted-foreground">{description}</p>
      </div>
      {children}
    </section>
  )
}

export function IntegrationsPage() {
  const api = useApi()
  useLiveTopics(TOPICS)
  const devices = useQuery(devicesQueryOptions(api)).data ?? []
  return (
    <>
      <PageHeader
        title="Integrations"
        description="Home Assistant, MCP servers and Hermes: how each is connected and what the assistant may use of it; what the speakers follow, and when they keep quiet."
      />
      <div className="flex flex-col gap-12">
        <Section title="Home Assistant" description="The home the assistant controls by voice: the connection, the areas and entities it sees, and what it uses most.">
          <HomeAssistantSection devices={devices} />
        </Section>
        <Section title="MCP servers" description="Tools from other servers. Each is off until you offer it, and one whose description changes is withdrawn until you acknowledge it.">
          <McpSection />
        </Section>
        <Section title="Hermes" description="Runs the background tasks; its tasks are on the Tasks page.">
          <HermesStatus />
        </Section>
        <Section title="Subscriptions" description="What each speaker follows in other systems, such as a coding agent's threads: it tells when one stops, asks or fails. A speaker's open questions and unread updates are on its page.">
          <SubscriptionsSection devices={devices} />
        </Section>
        <Section title="Busy" description="While someone is in a call, a meeting, screensharing or not to be disturbed, the speakers hold what nobody asked for and tell it once busy ends. Alarms, timers and what you ask still sound.">
          <BusySection devices={devices} />
        </Section>
      </div>
    </>
  )
}

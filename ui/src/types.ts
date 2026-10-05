export type Health = 'OK' | 'Warning' | 'Critical' | 'Unknown'
export type PowerState = 'On' | 'Off' | 'Unknown'
export type Severity = 'ok' | 'warning' | 'critical' | 'noreading'

export interface Problem {
  severity: Health
  source: string
  message: string
}

export interface Condition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  reason: string
  message: string
  lastTransitionTime: string
}

export interface BMCStatus {
  health?: Health
  powerState?: PowerState
  powerWatts?: number
  inletTemperature?: number
  device?: {
    manufacturer?: string; product?: string; version?: string; serialNumber?: string; partNumber?: string
    boardProduct?: string; boardSerial?: string; chassisType?: string; chassisSerial?: string
  }
  controller?: { firmwareVersion?: string; ipmiVersion?: string; manufacturerID?: string; productID?: string; guid?: string }
  network?: { channel?: number; ipAddress?: string; netmask?: string; gateway?: string; macAddress?: string; source?: string; vlan?: string }
  chassis?: { powerRestorePolicy?: string; lastPowerEvent?: string; intrusionActive?: boolean; faults?: string[] }
  sel?: { entries?: number; usedPercent?: number; lastAddTime?: string }
  sensors?: { total?: number; ok?: number; warning?: number; critical?: number; noReading?: number }
  problems?: Problem[]
  agentVersion?: string
  lastUpdated?: string
  conditions?: Condition[]
}

export interface BMCSpec {
  nodeName: string
  address?: string
  protocol?: 'Redfish' | 'IPMI'
  credentialsRef?: { name: string; namespace?: string }
  insecureSkipVerify?: boolean
}

export interface NodeInfo {
  ready: boolean
  unschedulable: boolean
  roles: string[] | null
  internalIP: string
  kubeletVersion: string
  osImage: string
  kernelVersion: string
  cpu: string
  memory: string
  gpus: number
  gpuModel?: string
}

export interface View {
  name: string
  created: string
  spec: BMCSpec
  status: BMCStatus
  node?: NodeInfo
  agent?: { pod: string; ip: string; ready: boolean }
  stale: boolean
  oobConfigured: boolean
}

export interface Thresholds {
  lnr?: number; lcr?: number; lnc?: number; unc?: number; ucr?: number; unr?: number
}

export interface Sensor {
  name: string
  type: string
  value?: number
  unit?: string
  reading: string
  status: string
  severity: Severity
  entity?: string
  thresholds?: Thresholds
}

export interface SELEvent {
  id: string
  timestamp: string
  sensor: string
  event: string
  asserted: boolean
  detail?: string
}

export interface Snapshot {
  node: string
  collectedAt: string
  sensors: Sensor[] | null
  events: SELEvent[] | null
  errors?: Record<string, string>
}

export interface Config {
  version: string
  powerActions: boolean
  demo: boolean
  clusterName?: string
}

export const powerActions = ['On', 'GracefulShutdown', 'GracefulRestart', 'ForceRestart', 'PowerCycle', 'ForceOff'] as const
export type PowerAction = (typeof powerActions)[number]

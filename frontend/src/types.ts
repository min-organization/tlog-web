export interface Session {
  rec: string
  user: string
  time: string // 格式化后的时间字符串
  summary: string
}

export interface SessionListResp {
  total: number
  page: number
  page_size: number
  items: Session[]
}

export interface QueryParams {
  page: number
  page_size: number
  user: string
  q: string
  date_from: string
  date_to: string
}

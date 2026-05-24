export class Notification {
  id: string
  title: string
  message: string
  level: string

  constructor(init: Partial<Omit<Notification, never>> = {}) {
    this.id = (init as Record<string, string>).id ?? String(Math.random())
    this.title = (init as Record<string, string>).title ?? ''
    this.message = (init as Record<string, string>).message ?? ''
    this.level = (init as Record<string, string>).level ?? 'info'
  }
}

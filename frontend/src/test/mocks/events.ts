export class Notification {
  id: string
  title: string
  message: string
  level: string

  constructor(init: Partial<Notification> = {}) {
    this.id = init.id ?? String(Math.random())
    this.title = init.title ?? ''
    this.message = init.message ?? ''
    this.level = init.level ?? 'info'
  }
}

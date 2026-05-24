import '@testing-library/jest-dom'
import { vi, beforeEach } from 'vitest'

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: vi.fn().mockImplementation(() => vi.fn()),
    Off: vi.fn(),
    Emit: vi.fn(),
  },
  Window: {
    Minimise: vi.fn(),
  },
  Application: {
    Quit: vi.fn(),
  },
}))

vi.mock('@wailsio/runtime/types/events', () => ({}))

beforeEach(() => {
  vi.clearAllMocks()
})

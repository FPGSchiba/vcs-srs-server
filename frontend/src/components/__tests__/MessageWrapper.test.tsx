import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import MessageWrapper from '../MessageWrapper'

describe('MessageWrapper', () => {
    it('renders without crashing', () => {
        const { container } = render(<MessageWrapper />)
        expect(container).toBeInTheDocument()
    })

    it('renders no notifications by default (Snackbar closed)', () => {
        render(<MessageWrapper />)
        // No notification alerts should be visible when list is empty
        expect(document.querySelector('[role="alert"]')).not.toBeInTheDocument()
    })
})

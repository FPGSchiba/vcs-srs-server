import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import FrequencyForm from '../FrequencyForm'

describe('FrequencyForm', () => {
    it('renders without crashing', () => {
        const { container } = render(<FrequencyForm onSubmit={vi.fn()} onCancel={vi.fn()} />)
        expect(container).toBeInTheDocument()
    })

    it('renders the frequency input field', () => {
        render(<FrequencyForm onSubmit={vi.fn()} onCancel={vi.fn()} />)
        expect(screen.getByLabelText(/Frequency/i)).toBeInTheDocument()
    })

    it('renders the frequency type select with Global and Test options', () => {
        render(<FrequencyForm onSubmit={vi.fn()} onCancel={vi.fn()} />)
        expect(screen.getByText('Global')).toBeInTheDocument()
        expect(screen.getByText('Test')).toBeInTheDocument()
    })

    it('renders the Cancel and Add buttons', () => {
        render(<FrequencyForm onSubmit={vi.fn()} onCancel={vi.fn()} />)
        expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Add' })).toBeInTheDocument()
    })

    it('calls onCancel when Cancel button is clicked', async () => {
        const onCancel = vi.fn()
        render(<FrequencyForm onSubmit={vi.fn()} onCancel={onCancel} />)
        await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
        expect(onCancel).toHaveBeenCalledOnce()
    })

    it('shows helper text with format hint', () => {
        render(<FrequencyForm onSubmit={vi.fn()} onCancel={vi.fn()} />)
        expect(screen.getByText(/Format: 123\.123/)).toBeInTheDocument()
    })
})

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from '../App'
import { approved, NOW } from '../test/fixtures'
import { install } from '../test/server'

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  install()
  window.history.replaceState(null, '', `/changes/${approved.id}`)
})

afterEach(() => {
  cleanup() // unmount before the stubs go, or a late load would hit the real globals
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function openFromEvidence(step = 'test') {
  render(<App />)
  await screen.findByRole('complementary', { name: 'Verdict' })
  const row = screen.getByText(step, { selector: '.step .name' }).closest('button')!
  fireEvent.click(row)
  const panel = await screen.findByRole('region', { name: 'Step log' })
  await within(panel).findByText(/go test/)
  return panel
}

describe('Log panel', () => {
  it('opens from an evidence row with the step header and numbered lines', async () => {
    const panel = await openFromEvidence()
    expect(within(panel).getByText('test', { selector: '.log-step' })).toBeTruthy()
    expect(panel.textContent).toContain(approved.id)
    expect(panel.textContent).toContain('run_8h2k4m1qz2bb')
    expect(panel.textContent).toContain('iteration')
    expect(panel.textContent).toContain('exit code')
    const lines = panel.querySelectorAll('[data-line]')
    expect(lines.length).toBe(6)
    expect(lines[0].textContent).toContain('1')
    expect(lines[0].textContent).toContain('$ go test ./... -count=1')
    expect(lines[2].className).toContain('red')
    expect(panel.textContent).toContain('6 lines')
    expect(panel.textContent).toContain('Step succeeded. Full log, fetched on open.')
  })

  it('searches with a match counter, Enter and Next move the current match, Wrap toggles', async () => {
    const panel = await openFromEvidence()
    const search = within(panel).getByLabelText('Search log')
    fireEvent.change(search, { target: { value: 'fail' } })
    expect(within(panel).getByText('1/3')).toBeTruthy()
    expect(panel.querySelector('[data-m="0"]')!.className).toContain('hit-strong')
    fireEvent.keyDown(search, { key: 'Enter' })
    expect(within(panel).getByText('2/3')).toBeTruthy()
    fireEvent.click(within(panel).getByRole('button', { name: 'Next match' }))
    expect(within(panel).getByText('3/3')).toBeTruthy()
    fireEvent.click(within(panel).getByRole('button', { name: 'Next match' }))
    expect(within(panel).getByText('1/3')).toBeTruthy()
    fireEvent.change(search, { target: { value: 'nothing here' } })
    expect(within(panel).getByText('0/0')).toBeTruthy()
    const wrap = within(panel).getByRole('button', { name: 'Wrap' })
    expect(wrap.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(wrap)
    expect(wrap.getAttribute('aria-pressed')).toBe('true')
  })

  it('closes with Escape and with the close button, and / focuses its search while open', async () => {
    const panel = await openFromEvidence()
    fireEvent.keyDown(window, { key: '/' })
    expect(document.activeElement).toBe(within(panel).getByLabelText('Search log'))
    ;(document.activeElement as HTMLElement).blur()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByRole('region', { name: 'Step log' })).toBeNull()
    expect(window.location.pathname).toBe(`/changes/${approved.id}`)
    fireEvent.click(screen.getByText('validate', { selector: '.step .name' }).closest('button')!)
    const again = await screen.findByRole('region', { name: 'Step log' })
    expect(within(again).getByText('validate', { selector: '.log-step' })).toBeTruthy()
    expect(within(again).getByText('validator')).toBeTruthy()
    fireEvent.click(within(again).getByRole('button', { name: 'Close log' }))
    expect(screen.queryByRole('region', { name: 'Step log' })).toBeNull()
  })

  it('opens from a criterion evidence path and from the verdict panel', async () => {
    render(<App />)
    const verdict = await screen.findByRole('complementary', { name: 'Verdict' })
    fireEvent.click(screen.getAllByRole('button', { name: 'evidence/ai-review/review.json' })[0])
    let panel = await screen.findByRole('region', { name: 'Step log' })
    expect(within(panel).getByText('ai-review', { selector: '.log-step' })).toBeTruthy()
    fireEvent.click(within(panel).getByRole('button', { name: 'Close log' }))
    fireEvent.click(within(verdict).getByRole('button', { name: /validate/ }))
    panel = await screen.findByRole('region', { name: 'Step log' })
    await waitFor(() => expect(within(panel).getByText('validate', { selector: '.log-step' })).toBeTruthy())
  })
})

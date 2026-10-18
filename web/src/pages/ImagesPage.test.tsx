import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from '../App'
import { NOW } from '../test/fixtures'
import { install, type FakeDB } from '../test/server'
import { buildPoll } from './ImagesPage'

let db: FakeDB

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  db = install()
  window.history.replaceState(null, '', '/images')
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('Images page', () => {
  it('lists every image with its status and users, shipped ones first', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Images' })
    expect(screen.getByText('4 images · 1 missing')).toBeTruthy()
    const ai = await screen.findByLabelText('Image plumbix-ai-validator:dev')
    expect(ai.textContent).toContain('shipped by Plumbix')
    expect(ai.textContent).toContain('validator')
    expect(ai.textContent).toContain('Built outside Plumbix')
    expect(ai.textContent).toContain('21 MB')
    expect(ai.textContent).toContain('Used by ledger-api: ai-grok')
    const ref = screen.getByLabelText('Image plumbix-reference-validator:dev')
    expect(ref.textContent).toContain('Built by this server (9ffe6c6)')
    const node = screen.getByLabelText('Image node:24-alpine')
    expect(node.textContent).toContain('Not present locally; Docker pulls it on first use.')
    expect(within(node).queryByRole('button')).toBeNull()
    const golang = screen.getByLabelText('Image golang:1.27')
    expect(golang.textContent).toContain('Used by plumbix: build · plumbix: test')
    const cards = screen.getAllByLabelText(/^Image /).map((c) => c.getAttribute('aria-label'))
    expect(cards.slice(0, 2)).toEqual(['Image plumbix-ai-validator:dev', 'Image plumbix-reference-validator:dev'])
    expect(document.body.textContent).toContain('only a signed-in person can start one')
  })

  it('rebuilds a shipped image and follows its log until it finishes', async () => {
    buildPoll.every = 20
    try {
      render(<App />)
      const ai = await screen.findByLabelText('Image plumbix-ai-validator:dev')
      fireEvent.click(within(ai).getByRole('button', { name: 'Rebuild' }))
      await waitFor(() => expect(db.imageBuilds).toHaveLength(1))
      expect(db.imageBuilds[0].image).toBe('plumbix-ai-validator:dev')
      await within(ai).findByLabelText('Build log for plumbix-ai-validator:dev')
      expect(within(ai).getByRole('button', { name: 'Building…' }).hasAttribute('disabled')).toBe(true)
      // The build ends; the page shows the result and reloads the list.
      db.imageBuilds[0].log += 'Step 1/9 : FROM golang:1.27-alpine\nplumbix: built plumbix-ai-validator:dev\n'
      db.imageBuilds[0].status = 'succeeded'
      db.images[0] = { ...db.images[0], status: 'current', version: '9ffe6c6', created: NOW.toISOString() }
      await waitFor(() => expect(screen.getByLabelText('Image plumbix-ai-validator:dev').textContent).toContain('Built by this server (9ffe6c6)'))
      expect(screen.getByLabelText('Build log for plumbix-ai-validator:dev').textContent).toContain('built plumbix-ai-validator:dev')
      expect(within(screen.getByLabelText('Image plumbix-ai-validator:dev')).getByRole('button', { name: 'Rebuild' })).toBeTruthy()
    } finally {
      buildPoll.every = 1500
    }
  })

  it('has an Images entry in the sidebar', async () => {
    window.history.replaceState(null, '', '/')
    render(<App />)
    await screen.findByRole('list', { name: 'Changes' })
    fireEvent.click(screen.getByRole('button', { name: 'Images' }))
    await screen.findByRole('heading', { name: 'Images' })
    expect(window.location.pathname).toBe('/images')
  })
})

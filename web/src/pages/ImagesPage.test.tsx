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

  it('adds an own image from its Dockerfile, edits and saves it, builds it, and deletes it', async () => {
    buildPoll.every = 20
    try {
      render(<App />)
      await screen.findByRole('heading', { name: 'Images' })
      const add = within(screen.getByLabelText('Add an image'))
      expect((add.getByLabelText('New image Dockerfile') as HTMLTextAreaElement).value).toContain('FROM alpine:3.20')
      fireEvent.change(add.getByLabelText('Name'), { target: { value: 'Lint Tools' } })
      fireEvent.click(add.getByRole('button', { name: 'Add image' }))
      await screen.findByText(/name must be lowercase letters/)
      fireEvent.change(add.getByLabelText('Name'), { target: { value: 'lint-tools' } })
      fireEvent.change(add.getByLabelText('New image Dockerfile'), { target: { value: 'FROM alpine:3.20\nRUN apk add --no-cache shellcheck' } })
      fireEvent.click(add.getByRole('button', { name: 'Add image' }))
      const card = await screen.findByLabelText('Image lint-tools:latest')
      expect(card.textContent).toContain('own image')
      expect(card.textContent).toContain('Not built yet')
      expect((within(card).getByLabelText('Dockerfile for lint-tools') as HTMLTextAreaElement).value).toContain('shellcheck')
      fireEvent.change(within(card).getByLabelText('Dockerfile for lint-tools'), { target: { value: 'FROM alpine:3.20\nRUN apk add --no-cache shellcheck shfmt\n' } })
      fireEvent.click(within(card).getByRole('button', { name: 'Save Dockerfile' }))
      await waitFor(() => expect(db.images.find((i) => i.ref === 'lint-tools:latest')?.dockerfile).toContain('shfmt'))
      await screen.findByText('Saved. Build to apply it.')
      fireEvent.click(within(screen.getByLabelText('Image lint-tools:latest')).getByRole('button', { name: 'Build' }))
      await waitFor(() => expect(db.imageBuilds.find((b) => b.image === 'lint-tools:latest')).toBeTruthy())
      const b = db.imageBuilds.find((x) => x.image === 'lint-tools:latest')!
      b.status = 'succeeded'
      b.log += 'plumbix: built lint-tools:latest\n'
      const own = db.images.find((i) => i.ref === 'lint-tools:latest')!
      // Meanwhile a pipeline started naming it; the reload after the build shows both.
      Object.assign(own, { present: true, status: 'current', created: NOW.toISOString(), size_bytes: 9_000_000, used_by: [{ repo_id: 'r_plumbix00001', repo: 'plumbix', step: 'lint', validator: false }] })
      await waitFor(() => expect(screen.getByLabelText('Image lint-tools:latest').textContent).toContain('Built from this Dockerfile'))
      expect(screen.getByLabelText('Image lint-tools:latest').textContent).toContain('Used by plumbix: lint')
      fireEvent.click(within(screen.getByLabelText('Image lint-tools:latest')).getByRole('button', { name: 'Delete' }))
      expect(screen.getByLabelText('Image lint-tools:latest').textContent).toContain('Still used by plumbix: lint. Delete anyway?')
      fireEvent.click(within(screen.getByLabelText('Image lint-tools:latest')).getByRole('button', { name: 'Delete lint-tools' }))
      await waitFor(() => expect(screen.queryByLabelText('Image lint-tools:latest')).toBeNull())
      expect(db.images.some((i) => i.ref === 'lint-tools:latest')).toBe(false)
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

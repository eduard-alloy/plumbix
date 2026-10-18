import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

afterEach(() => {
  cleanup()
})

// A loaded runner (four pipeline containers on one machine) can take more
// than the default second for a page's first load; waiting longer does not
// slow a passing test.
import { configure } from '@testing-library/react'
configure({ asyncUtilTimeout: 5000 })

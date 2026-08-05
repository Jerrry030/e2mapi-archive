// Shared vitest setup for the console test suite.
//
// The page tests render real antd / pro-components trees into jsdom. A first
// render of a page (ProCard + ModalForm + antd's CSS-in-JS style generation)
// costs seconds of CPU, and the whole suite runs in a single worker on small
// machines, so those seconds stretch further under load. Testing Library's
// 1s default for findBy*/waitFor is a timing budget, not a correctness one:
// blowing it means the box was busy, not that the component misbehaved. Give
// the async utilities enough room that only real failures fail.
import { configure } from '@testing-library/react'

configure({ asyncUtilTimeout: 5_000 })

import { ref } from 'vue'
import { describe, expect, it } from 'vitest'
import type { ChatFlowGraph, ChatNode } from '@/services/api'
import { useFlowGraphSimulation } from './useFlowGraphSimulation'

function node(id: string, type: ChatNode['type'], config: ChatNode['config'] = {}): ChatNode {
  return { id, type, label: id, position: { x: 0, y: 0 }, config }
}

describe('interactive prompts in Preview', () => {
  it.each([
    { numbers: ['1', '2'], outside: '8' },
    { numbers: ['3', '4', '5'], outside: '1' },
    { numbers: ['6', '7', '8'], outside: '3' },
  ])('handles taps, typing and retries for $numbers', async ({ numbers, outside }) => {
    const buttons = [...numbers, 'menu', 'agent'].map(id => ({ id, title: `Choice ${id}` }))
    const graph: ChatFlowGraph = {
      version: 2,
      entry_node: 'submenu',
      nodes: [
        node('submenu', 'prompt', {
          body: 'Choose an option',
          buttons,
          store_as: 'choice',
          validation_regex: `^(${buttons.map(button => button.id).join('|')})$`,
          validation_error: 'Choose from this menu',
          max_retries: 2,
        }),
        node('route', 'end'),
        node('handoff_offer', 'end', { message: 'Talk to support' }),
      ],
      edges: [
        { from: 'submenu', to: 'route', condition: 'default' },
        { from: 'submenu', to: 'handoff_offer', condition: 'max_retries' },
      ],
    }
    const preview = useFlowGraphSimulation(ref(graph), ref({}))
    await preview.startSimulation()
    expect(preview.expectedInputType.value).toBe('text')
    expect(preview.state.messages.at(-1)?.buttons).toEqual(buttons)
    await preview.processUserInput(outside)
    expect(preview.state.messages.at(-1)).toMatchObject({ buttons, isValidationError: true })
    await preview.processUserInput(outside)
    expect(preview.state.currentStepName).toBe('handoff_offer')
    expect(preview.state.messages.some(message => message.content === 'Talk to support')).toBe(true)

    for (const input of [buttons[0]!, ...numbers, 'menu', 'agent']) {
      await preview.startSimulation()
      await preview.processUserInput(input)
      expect(preview.state.variables.choice).toBe(typeof input === 'string' ? input : input.id)
      expect(preview.state.currentStepName).toBe('route')
    }
  })
})

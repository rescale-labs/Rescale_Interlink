import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { SingleJobTab } from './SingleJobTab'
import { useSingleJobStore } from '../../stores/singleJobStore'

afterEach(() => {
  cleanup()
  useSingleJobStore.getState().reset()
})

// Single Job's list of selected input files and folders, the list PUR's Common
// Input Files share: counts, sizes, the total, remove and Clear All.
describe('SingleJobTab input files list', () => {
  it('lists the selection with sizes, and removes or clears it', () => {
    useSingleJobStore.setState({
      state: 'jobConfigured',
      inputMode: 'localFiles',
      localFiles: ['/scratch/deck.inp', '/scratch/mesh'],
      fileInfoMap: {
        '/scratch/deck.inp': { path: '/scratch/deck.inp', name: 'deck.inp', isDir: false, size: 2048, fileCount: 0 },
        '/scratch/mesh': { path: '/scratch/mesh', name: 'mesh', isDir: true, size: 3 * 1024 * 1024, fileCount: 3 },
      },
    })

    render(<SingleJobTab />)

    expect(screen.getByText('1 file, 1 folder')).toBeInTheDocument()
    expect(screen.getByTitle('/scratch/deck.inp')).toHaveTextContent('deck.inp')
    expect(screen.getByText('2 KB')).toBeInTheDocument()
    expect(screen.getByText('3 files')).toBeInTheDocument()
    expect(screen.getByText('3 MB')).toBeInTheDocument() // the total

    fireEvent.click(screen.getAllByTitle('Remove')[0])
    expect(useSingleJobStore.getState().localFiles).toEqual(['/scratch/mesh'])
    fireEvent.click(screen.getByRole('button', { name: 'Clear All' }))
    expect(useSingleJobStore.getState().localFiles).toEqual([])
  })
})

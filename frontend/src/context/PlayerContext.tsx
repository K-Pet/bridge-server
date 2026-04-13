import { createContext, useContext, useCallback, useRef, useState, useEffect, type ReactNode } from 'react'
import type { Song } from '../lib/subsonic'
import { streamUrl } from '../lib/subsonic'

export interface PlayerState {
  currentSong: Song | null
  queue: Song[]
  queueIndex: number
  isPlaying: boolean
  currentTime: number
  duration: number
  volume: number
  shuffle: boolean
  repeat: 'off' | 'all' | 'one'
}

interface PlayerActions {
  playSong: (song: Song, queue?: Song[]) => void
  playAlbum: (songs: Song[], startIndex?: number) => void
  togglePlay: () => void
  next: () => void
  previous: () => void
  seek: (time: number) => void
  setVolume: (vol: number) => void
  toggleShuffle: () => void
  toggleRepeat: () => void
  addToQueue: (song: Song) => void
  clearQueue: () => void
}

type PlayerContextType = PlayerState & PlayerActions

const PlayerContext = createContext<PlayerContextType | null>(null)

export function usePlayer(): PlayerContextType {
  const ctx = useContext(PlayerContext)
  if (!ctx) throw new Error('usePlayer must be used within PlayerProvider')
  return ctx
}

export function PlayerProvider({ children }: { children: ReactNode }) {
  const audioRef = useRef<HTMLAudioElement | null>(null)
  const [currentSong, setCurrentSong] = useState<Song | null>(null)
  const [queue, setQueue] = useState<Song[]>([])
  const [queueIndex, setQueueIndex] = useState(0)
  const [isPlaying, setIsPlaying] = useState(false)
  const [currentTime, setCurrentTime] = useState(0)
  const [duration, setDuration] = useState(0)
  const [volume, setVolumeState] = useState(0.8)
  const [shuffle, setShuffle] = useState(false)
  const [repeat, setRepeat] = useState<'off' | 'all' | 'one'>('off')

  // Initialize audio element once
  useEffect(() => {
    const audio = new Audio()
    audio.volume = 0.8
    audioRef.current = audio

    audio.addEventListener('timeupdate', () => setCurrentTime(audio.currentTime))
    audio.addEventListener('durationchange', () => setDuration(audio.duration))
    audio.addEventListener('ended', () => handleEnded())
    audio.addEventListener('play', () => setIsPlaying(true))
    audio.addEventListener('pause', () => setIsPlaying(false))

    return () => {
      audio.pause()
      audio.src = ''
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const handleEnded = useCallback(() => {
    if (repeat === 'one') {
      const audio = audioRef.current
      if (audio) {
        audio.currentTime = 0
        audio.play()
      }
      return
    }

    // Auto-advance
    setQueue(q => {
      setQueueIndex(idx => {
        const nextIdx = idx + 1
        if (nextIdx < q.length) {
          loadAndPlay(q[nextIdx])
          return nextIdx
        } else if (repeat === 'all' && q.length > 0) {
          loadAndPlay(q[0])
          return 0
        }
        setIsPlaying(false)
        return idx
      })
      return q
    })
  }, [repeat])

  // Re-attach ended listener when repeat changes
  useEffect(() => {
    const audio = audioRef.current
    if (!audio) return
    const handler = () => handleEnded()
    audio.addEventListener('ended', handler)
    return () => audio.removeEventListener('ended', handler)
  }, [handleEnded])

  function loadAndPlay(song: Song) {
    const audio = audioRef.current
    if (!audio) return
    audio.src = streamUrl(song.id)
    audio.play().catch(() => {})
    setCurrentSong(song)
  }

  const playSong = useCallback((song: Song, newQueue?: Song[]) => {
    if (newQueue) {
      setQueue(newQueue)
      setQueueIndex(newQueue.findIndex(s => s.id === song.id))
    } else {
      setQueue([song])
      setQueueIndex(0)
    }
    loadAndPlay(song)
  }, [])

  const playAlbum = useCallback((songs: Song[], startIndex = 0) => {
    if (songs.length === 0) return
    setQueue(songs)
    setQueueIndex(startIndex)
    loadAndPlay(songs[startIndex])
  }, [])

  const togglePlay = useCallback(() => {
    const audio = audioRef.current
    if (!audio) return
    if (audio.paused) {
      audio.play().catch(() => {})
    } else {
      audio.pause()
    }
  }, [])

  const next = useCallback(() => {
    if (queue.length === 0) return
    let nextIdx: number
    if (shuffle) {
      nextIdx = Math.floor(Math.random() * queue.length)
    } else {
      nextIdx = queueIndex + 1
      if (nextIdx >= queue.length) {
        if (repeat === 'all') nextIdx = 0
        else return
      }
    }
    setQueueIndex(nextIdx)
    loadAndPlay(queue[nextIdx])
  }, [queue, queueIndex, shuffle, repeat])

  const previous = useCallback(() => {
    const audio = audioRef.current
    if (!audio) return
    // If more than 3 seconds in, restart current song
    if (audio.currentTime > 3) {
      audio.currentTime = 0
      return
    }
    if (queueIndex > 0) {
      const prevIdx = queueIndex - 1
      setQueueIndex(prevIdx)
      loadAndPlay(queue[prevIdx])
    }
  }, [queue, queueIndex])

  const seek = useCallback((time: number) => {
    const audio = audioRef.current
    if (audio) audio.currentTime = time
  }, [])

  const setVolume = useCallback((vol: number) => {
    const audio = audioRef.current
    if (audio) audio.volume = vol
    setVolumeState(vol)
  }, [])

  const toggleShuffle = useCallback(() => setShuffle(s => !s), [])

  const toggleRepeat = useCallback(() => {
    setRepeat(r => r === 'off' ? 'all' : r === 'all' ? 'one' : 'off')
  }, [])

  const addToQueue = useCallback((song: Song) => {
    setQueue(q => [...q, song])
  }, [])

  const clearQueue = useCallback(() => {
    setQueue([])
    setQueueIndex(0)
  }, [])

  const value: PlayerContextType = {
    currentSong, queue, queueIndex, isPlaying, currentTime, duration, volume, shuffle, repeat,
    playSong, playAlbum, togglePlay, next, previous, seek, setVolume,
    toggleShuffle, toggleRepeat, addToQueue, clearQueue,
  }

  return (
    <PlayerContext.Provider value={value}>
      {children}
    </PlayerContext.Provider>
  )
}

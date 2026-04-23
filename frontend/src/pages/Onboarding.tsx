import { useCallback, useEffect, useState } from 'react'
import { getSupabase } from '../lib/supabase'
import { autoPair, type OnboardingStatus } from '../lib/api'
import bridgeLogo from '../assets/icons/Bridge-Main-Logo.svg'

interface Props {
  status: OnboardingStatus
  userId: string
  onComplete: () => void
}

type Step = 'profile' | 'pair' | 'done'

export default function Onboarding({ status, userId, onComplete }: Props) {
  const needsProfile = !status.profile_complete
  const needsPair = !status.server_paired

  const initialStep: Step = needsProfile ? 'profile' : needsPair ? 'pair' : 'done'
  const [step, setStep] = useState<Step>(initialStep)

  // If everything is already done, skip immediately
  useEffect(() => {
    if (!needsProfile && !needsPair) onComplete()
  }, [needsProfile, needsPair, onComplete])

  if (step === 'profile') {
    return (
      <OnboardingShell step={1} totalSteps={needsPair ? 2 : 1}>
        <ProfileStep
          userId={userId}
          existingUsername={status.profile?.username ?? ''}
          onNext={() => {
            if (needsPair) setStep('pair')
            else setStep('done')
          }}
        />
      </OnboardingShell>
    )
  }

  if (step === 'pair') {
    return (
      <OnboardingShell step={needsProfile ? 2 : 1} totalSteps={needsProfile ? 2 : 1}>
        <PairStep
          autoPairAvailable={status.auto_pair_available}
          onNext={() => setStep('done')}
        />
      </OnboardingShell>
    )
  }

  // "done" step — brief success then redirect
  return (
    <OnboardingShell step={0} totalSteps={0}>
      <DoneStep onComplete={onComplete} />
    </OnboardingShell>
  )
}

// ── Shell ─────────────────────────────────────────────────────────────

function OnboardingShell({ step, totalSteps, children }: { step: number; totalSteps: number; children: React.ReactNode }) {
  return (
    <div className="onboarding-page">
      <div className="onboarding-card">
        <div className="onboarding-brand">
          <img src={bridgeLogo} alt="Bridge Music" className="brand-icon" />
          Bridge Music
        </div>
        {totalSteps > 0 && (
          <div className="onboarding-progress">
            {Array.from({ length: totalSteps }, (_, i) => (
              <div key={i} className={`progress-dot ${i < step ? 'done' : ''} ${i === step - 1 ? 'active' : ''}`} />
            ))}
          </div>
        )}
        {children}
      </div>
    </div>
  )
}

// ── Profile Step ──────────────────────────────────────────────────────

function ProfileStep({ userId, existingUsername, onNext }: {
  userId: string
  existingUsername: string
  onNext: () => void
}) {
  const [username, setUsername] = useState(existingUsername)
  const [fullName, setFullName] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const handleSubmit = useCallback(async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    const trimmed = username.trim()
    if (trimmed.length < 3) {
      setError('Username must be at least 3 characters')
      return
    }

    setSaving(true)
    const supabase = getSupabase()
    if (!supabase) {
      setError('Supabase not configured')
      setSaving(false)
      return
    }

    const updates: Record<string, string> = { username: trimmed }
    if (fullName.trim()) updates.full_name = fullName.trim()

    const { error: updateError } = await supabase
      .from('user_profiles')
      .update(updates)
      .eq('id', userId)

    if (updateError) {
      if (updateError.message.includes('unique') || updateError.message.includes('duplicate')) {
        setError('That username is already taken')
      } else {
        setError(updateError.message)
      }
      setSaving(false)
      return
    }

    onNext()
  }, [username, fullName, userId, onNext])

  return (
    <>
      <h2>Set up your profile</h2>
      <p className="onboarding-subtitle">
        Choose a username for your Bridge Music account. This will be visible to other users in the marketplace.
      </p>
      <form onSubmit={handleSubmit} className="onboarding-form">
        <label className="onboarding-label">
          Username
          <input
            type="text"
            placeholder="your-username"
            value={username}
            onChange={e => setUsername(e.target.value)}
            minLength={3}
            required
            autoFocus
          />
        </label>
        <label className="onboarding-label">
          Full name <span className="optional">(optional)</span>
          <input
            type="text"
            placeholder="Your Name"
            value={fullName}
            onChange={e => setFullName(e.target.value)}
          />
        </label>
        {error && <div className="error">{error}</div>}
        <button type="submit" disabled={saving}>
          {saving ? 'Saving...' : 'Continue'}
        </button>
      </form>
    </>
  )
}

// ── Pair Step ─────────────────────────────────────────────────────────

function PairStep({ autoPairAvailable, onNext }: {
  autoPairAvailable: boolean
  onNext: () => void
}) {
  const [pairing, setPairing] = useState(false)
  const [error, setError] = useState('')
  const [paired, setPaired] = useState(false)

  // Auto-pair immediately on mount if available
  useEffect(() => {
    if (!autoPairAvailable) return
    let cancelled = false

    async function doPair() {
      setPairing(true)
      setError('')
      try {
        const result = await autoPair()
        if (!cancelled && result.paired) {
          setPaired(true)
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Auto-pair failed')
        }
      } finally {
        if (!cancelled) setPairing(false)
      }
    }

    doPair()
    return () => { cancelled = true }
  }, [autoPairAvailable])

  if (!autoPairAvailable) {
    return (
      <>
        <h2>Link your server</h2>
        <p className="onboarding-subtitle">
          Auto-pairing isn't available because this server doesn't have an
          external URL configured. You can pair manually from the Settings
          page using a pairing code.
        </p>
        <button type="button" className="btn-primary" onClick={onNext}>
          Continue to app
        </button>
      </>
    )
  }

  if (paired) {
    return (
      <>
        <div className="onboarding-success-icon">
          <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="var(--green)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14" />
            <polyline points="22 4 12 14.01 9 11.01" />
          </svg>
        </div>
        <h2>Server linked</h2>
        <p className="onboarding-subtitle">
          This server is now paired to your Bridge Music account.
          Purchases from the marketplace will be delivered here automatically.
        </p>
        <button type="button" className="btn-primary" onClick={onNext}>
          Continue
        </button>
      </>
    )
  }

  if (pairing) {
    return (
      <>
        <h2>Linking your server</h2>
        <p className="onboarding-subtitle">
          Connecting this server to your Bridge Music marketplace account...
        </p>
        <div className="onboarding-spinner">
          <div className="spinner" />
        </div>
      </>
    )
  }

  // Error state — allow retry
  return (
    <>
      <h2>Link your server</h2>
      <p className="onboarding-subtitle">
        Something went wrong while pairing. You can retry or skip and pair
        manually from Settings later.
      </p>
      {error && <div className="error">{error}</div>}
      <div className="onboarding-actions">
        <button
          type="button"
          className="btn-primary"
          onClick={async () => {
            setPairing(true)
            setError('')
            try {
              const result = await autoPair()
              if (result.paired) setPaired(true)
            } catch (err) {
              setError(err instanceof Error ? err.message : 'Auto-pair failed')
            } finally {
              setPairing(false)
            }
          }}
        >
          Retry
        </button>
        <button type="button" className="btn-secondary" onClick={onNext}>
          Skip for now
        </button>
      </div>
    </>
  )
}

// ── Done Step ─────────────────────────────────────────────────────────

function DoneStep({ onComplete }: { onComplete: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onComplete, 1500)
    return () => clearTimeout(timer)
  }, [onComplete])

  return (
    <>
      <div className="onboarding-success-icon">
        <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="var(--green)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14" />
          <polyline points="22 4 12 14.01 9 11.01" />
        </svg>
      </div>
      <h2>You're all set!</h2>
      <p className="onboarding-subtitle">Taking you to your music library...</p>
    </>
  )
}

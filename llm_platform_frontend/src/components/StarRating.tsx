import { useState } from 'react';
import { api, errorMessage } from '../api/client';
import { useToast } from '../toast/context';

type TStarRatingProps = {
  runId: string;
  model: string;
  initial?: number;
};

// A 1–5 star rating for one model response. Persists to the backend on click.
const StarRating = ({ runId, model, initial = 0 }: TStarRatingProps) => {
  const toast = useToast();
  const [rating, setRating] = useState(initial);
  const [hover, setHover] = useState(0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(false);

  const submit = async (value: number) => {
    const prev = rating;
    setRating(value);
    setSaving(true);
    setError(false);
    try {
      await api.feedback(runId, model, value);
    } catch (e) {
      setRating(prev); // revert on failure
      setError(true); // keep the inline indicator
      toast.error(errorMessage(e)); // ...and show the actual reason
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex items-center gap-1 ml-1" title="Rate this response">
      {[1, 2, 3, 4, 5].map((star) => {
        const active = star <= (hover || rating);
        return (
          <button
            key={star}
            type="button"
            disabled={saving}
            onClick={() => submit(star)}
            onMouseEnter={() => setHover(star)}
            onMouseLeave={() => setHover(0)}
            className="text-sm leading-none transition-transform hover:scale-110 disabled:cursor-default"
            style={{ color: active ? '#F5A623' : 'var(--tertiary-border)' }}
            aria-label={`${star} star${star > 1 ? 's' : ''}`}
          >
            ★
          </button>
        );
      })}
      {error && <span className="text-[10px] text-error-text">save failed</span>}
    </div>
  );
};

export default StarRating;

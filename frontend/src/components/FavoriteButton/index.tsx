import { useEffect, useState } from 'react';
import { IconButton, Tooltip } from '@mui/material';
import StarIcon from '@mui/icons-material/Star';
import StarBorderIcon from '@mui/icons-material/StarBorder';
import { favoriteService } from '../../api/client';

interface FavoriteButtonProps {
  entityType: 'definition' | 'instance' | 'template';
  entityId: string;
  size?: 'small' | 'medium';
  initialFavorited?: boolean;
}

const FavoriteButton = ({ entityType, entityId, size = 'small', initialFavorited }: FavoriteButtonProps) => {
  const [isFavorite, setIsFavorite] = useState(initialFavorited ?? false);
  const [loading, setLoading] = useState(initialFavorited === undefined);

  useEffect(() => {
    if (initialFavorited !== undefined) return;
    let cancelled = false;
    const check = async () => {
      try {
        const result = await favoriteService.check(entityType, entityId);
        if (!cancelled) setIsFavorite(result);
      } catch {
        // Ignore — default to not-favorited
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    check();
    return () => { cancelled = true; };
  }, [entityType, entityId, initialFavorited]);

  const handleToggle = async (e: React.MouseEvent) => {
    e.stopPropagation();
    setLoading(true);
    try {
      if (isFavorite) {
        await favoriteService.remove(entityType, entityId);
        setIsFavorite(false);
      } else {
        await favoriteService.add(entityType, entityId);
        setIsFavorite(true);
      }
    } catch {
      // Ignore — keep current state
    } finally {
      setLoading(false);
    }
  };

  return (
    <Tooltip title={isFavorite ? 'Remove from favorites' : 'Add to favorites'}>
      <span>
        <IconButton
          size={size}
          onClick={handleToggle}
          disabled={loading}
          aria-label={isFavorite ? 'Remove from favorites' : 'Add to favorites'}
          sx={{ color: isFavorite ? 'warning.main' : 'action.disabled' }}
        >
          {isFavorite ? <StarIcon fontSize={size} /> : <StarBorderIcon fontSize={size} />}
        </IconButton>
      </span>
    </Tooltip>
  );
};

export default FavoriteButton;

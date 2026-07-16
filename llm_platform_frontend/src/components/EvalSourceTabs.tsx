import { Button } from '@meesho/merlin-ui-tailwind';

type TEvalSourceMode = 'csv' | 'prism';

type TEvalSourceTabsProps = {
  mode: TEvalSourceMode;
  onChange: (mode: TEvalSourceMode) => void;
};

const EvalSourceTabs = ({ mode, onChange }: TEvalSourceTabsProps) => (
  <div className="flex items-center gap-2">
    <Button variant={mode === 'csv' ? 'primary' : 'outline'} size="s" onClick={() => onChange('csv')}>
      CSV
    </Button>
    <Button variant={mode === 'prism' ? 'primary' : 'outline'} size="s" onClick={() => onChange('prism')}>
      Prism SQL
    </Button>
  </div>
);

export default EvalSourceTabs;

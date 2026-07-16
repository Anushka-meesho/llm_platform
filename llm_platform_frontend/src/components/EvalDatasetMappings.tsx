import EvalMappingEditor from './EvalMappingEditor';

type TEvalDatasetMappingsProps = {
  inputFields: string[];
  outputFields: string[];
  inputMapping: Record<string, string>;
  outputMapping: Record<string, string>;
  disabled: boolean;
  onInputChange: (field: string, value: string) => void;
  onOutputChange: (field: string, value: string) => void;
};

const EvalDatasetMappings = ({
  inputFields,
  outputFields,
  inputMapping,
  outputMapping,
  disabled,
  onInputChange,
  onOutputChange,
}: TEvalDatasetMappingsProps) => (
  <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
    <EvalMappingEditor
      title="Input columns"
      fields={inputFields}
      mapping={inputMapping}
      disabled={disabled}
      onChange={onInputChange}
    />
    <EvalMappingEditor
      title="Expected output columns"
      fields={outputFields}
      mapping={outputMapping}
      disabled={disabled}
      onChange={onOutputChange}
    />
  </div>
);

export default EvalDatasetMappings;

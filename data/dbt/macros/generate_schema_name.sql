{#- Models land in the schema their folder names (core, mart) as-is, so Cube
    reads mart.fin_journal_line and not <target>_mart.fin_journal_line. -#}
{% macro generate_schema_name(custom_schema_name, node) -%}
    {%- if custom_schema_name is none -%}{{ target.schema }}{%- else -%}{{ custom_schema_name | trim }}{%- endif -%}
{%- endmacro %}
